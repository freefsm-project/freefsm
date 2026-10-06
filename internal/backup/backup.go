// Package backup owns encrypted whole-instance transfer and crash recovery.
// Callers enforce administrator authorization and fresh account reauthentication.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

var ErrNotFound = errors.New("operation not found or expired")
var ErrInvalid = errors.New("invalid backup operation or confirmation")
var ErrRecoveryRequired = errors.New("backup recovery required before another operation can start")
var ErrUnsupportedRelease = errors.New("invalid backup build identity")

type BuildKind string

const (
	BuildRelease     BuildKind = "release"
	BuildDevelopment BuildKind = "development"
)

type Config struct {
	DSN, UploadDir, StateDir, Version, Commit string
	BuildKind                                 BuildKind
	// RecoveryOnly permits startup recovery on unsupported builds, but no transfers.
	RecoveryOnly bool
	Control      *instancecontrol.Control
	// ResetConnections must reset every application pool under maintenance after
	// schema replacement; pgxpool.Pool.Reset is appropriate. Required for restore.
	ResetConnections func()
	// ReportFailure receives bounded, sanitized diagnostics, never raw errors,
	// SQL, command arguments, paths, credentials or subprocess stderr.
	ReportFailure func(Diagnostic)
	// ActivitySink must persist idempotently by event Key and support recovery
	// before application pools exist. Nil preserves unaudited legacy callers.
	ActivitySink func(context.Context, ActivityEvent) error
}
type Operation struct {
	ID, Kind, Phase, Error, SourceName, Version, Commit, ArchiveDigest string
	BuildKind                                                          BuildKind
	CapturedAt, ExpiresAt                                              time.Time
	Failure                                                            Failure
}
type job struct {
	Operation
	actor         int64
	activityActor ActivityActor
	committed     bool
	outcome       ActivityEvent
	dir           string
	manifest      manifest
	readers       int
	active        bool
	lease         *os.File
}
type Manager struct {
	cfg        Config
	root       string
	mu         sync.Mutex
	jobs       map[string]*job
	wg         sync.WaitGroup
	closed     bool
	poolMu     sync.Mutex
	generation string
	// Set only by package tests, before jobs start. No environment/config bypass.
	checkpoint func(string) error
}
type journal struct {
	Phase, Recovery        string
	EmailDisabled          bool
	UploadDir, Destination string
	Start, Outcome         *ActivityEvent `json:",omitempty"`
}

const prereleaseIdentifier = `(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`

var releasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-` + prereleaseIdentifier + `(?:\.` + prereleaseIdentifier + `)*)?$`)

func New(cfg Config) (*Manager, error) {
	kind, valid := buildKind(cfg.BuildKind, cfg.Version, cfg.Commit)
	if !cfg.RecoveryOnly && !valid {
		return nil, ErrUnsupportedRelease
	}
	cfg.BuildKind = kind
	if cfg.Control == nil || cfg.DSN == "" || cfg.UploadDir == "" || cfg.StateDir == "" {
		return nil, ErrInvalid
	}
	var e error
	cfg.UploadDir, e = filepath.Abs(cfg.UploadDir)
	if e != nil {
		return nil, e
	}
	cfg.StateDir, e = filepath.Abs(cfg.StateDir)
	if e != nil {
		return nil, e
	}
	if cfg.StateDir != cfg.Control.StateDir() || cfg.UploadDir == "/" || within(cfg.UploadDir, cfg.StateDir) || within(cfg.StateDir, cfg.UploadDir) {
		return nil, engineFailure(FailureFileReferences, "state and upload roots must be disjoint, non-root directories")
	}
	if e = checkDirectoryPath(cfg.UploadDir); e != nil {
		return nil, e
	}
	if e = privateDir(cfg.StateDir); e != nil {
		return nil, e
	}
	root := filepath.Join(cfg.StateDir, "backup")
	if e = privateDir(root); e != nil {
		return nil, e
	}
	if e = os.Chmod(root, 0700); e != nil {
		return nil, e
	}
	return &Manager{cfg: cfg, root: root, jobs: map[string]*job{}}, syncDir(cfg.StateDir)
}

// Missing kinds are legacy release identities, never inferred development.
// Explicit development identities may be dirty, untagged or unidentified.
func buildKind(kind BuildKind, version, commit string) (BuildKind, bool) {
	if kind == BuildDevelopment {
		return kind, true
	}
	if kind != "" && kind != BuildRelease {
		return kind, false
	}
	return BuildRelease, releasePattern.MatchString(version) &&
		!regexp.MustCompile(`(?i)dev|dirty|-[0-9]+-g[0-9a-f]+$`).MatchString(version) &&
		regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`).MatchString(commit)
}
func within(a, b string) bool {
	r, e := filepath.Rel(a, b)
	return e == nil && (r == "." || safeRelative(r))
}
func (m *Manager) journalPath() string { return filepath.Join(m.root, "journal.json") }
func (m *Manager) destinationIdentity() string {
	h := sha256.Sum256([]byte(m.cfg.DSN))
	return hex.EncodeToString(h[:])
}
func (m *Manager) writeJournal(j journal) error {
	j.UploadDir = m.cfg.UploadDir
	j.Destination = m.destinationIdentity()
	return atomicJSON(m.journalPath(), j)
}

// Caller must hold OperationLock. Lstat also rejects dangling journal symlinks.
func (m *Manager) requireNoJournal() error {
	if _, err := os.Lstat(m.pendingPath()); !errors.Is(err, os.ErrNotExist) {
		return ErrRecoveryRequired
	}
	if _, err := os.Lstat(m.journalPath()); !errors.Is(err, os.ErrNotExist) {
		return ErrRecoveryRequired
	}
	return nil
}
func (m *Manager) check(point string) error {
	if m.checkpoint != nil {
		return m.checkpoint(point)
	}
	return nil
}
func (m *Manager) clear() error {
	if e := m.cfg.Control.ClearMaintenance(); e != nil {
		_ = m.cfg.Control.KeepClosed()
		return e
	}
	return nil
}

// Recover MUST run before migrations, admissions and workers. It also discards
// interrupted predestructive transfers. Never call while this manager has jobs.
func (m *Manager) Recover(ctx context.Context) (retErr error) {
	unlock, e := m.cfg.Control.OperationLock()
	if e != nil {
		return e
	}
	defer unlock()
	defer func() {
		if retErr != nil {
			retErr = classified(FailureRecoveryRequired, retErr)
			m.reportFailure(retErr, "recovering")
		}
	}()
	release, e := m.cfg.Control.Maintenance(ctx)
	if e != nil {
		return e
	}
	defer release()
	b, e := os.ReadFile(m.journalPath())
	if e == nil {
		var j journal
		if e = json.Unmarshal(b, &j); e != nil {
			return e
		}
		if e = m.resolve(ctx, j); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = m.replayPending(ctx); e != nil {
		return e
	}
	entries, e := os.ReadDir(m.root)
	if e != nil {
		return e
	}
	for _, v := range entries {
		if v.IsDir() {
			if e = cleanupAbandoned(filepath.Join(m.root, v.Name())); e != nil {
				return e
			}
		}
	}
	if e = syncDir(m.root); e != nil {
		return e
	}
	return m.clear()
}
func (m *Manager) resolve(ctx context.Context, j journal) error {
	if j.UploadDir != m.cfg.UploadDir || j.Destination != m.destinationIdentity() {
		return engineFailure(FailureRecoveryRequired, "recovery destination configuration changed")
	}
	if !safeRelative(j.Recovery) || filepath.Base(j.Recovery) != j.Recovery {
		return engineFailure(FailureRecoveryRequired, "invalid recovery journal")
	}
	dir := filepath.Join(m.root, j.Recovery)
	switch j.Phase {
	case "replacing":
		if e := m.check("rollback-start"); e != nil {
			return e
		}
		if e := mVerifyRecovery(dir); e != nil {
			return e
		}
		if e := m.apply(ctx, dir, dir); e != nil {
			return e
		}
		if e := m.replaceFiles(filepath.Join(dir, "uploads")); e != nil {
			return e
		}
		if j.EmailDisabled {
			if e := m.cfg.Control.DisableEmail(); e != nil {
				return e
			}
		} else {
			if e := m.cfg.Control.EnableEmail(); e != nil {
				return e
			}
		}
		if m.cfg.ResetConnections != nil {
			m.cfg.ResetConnections()
		}
		j.Phase = "rolled-back"
		if e := m.writeJournal(j); e != nil {
			return e
		}
	case "capturing", "committed", "rolled-back":
	default:
		return engineFailure(FailureRecoveryRequired, "unknown recovery phase")
	}
	if j.Start != nil {
		if e := m.publish(ctx, *j.Start); e != nil {
			return e
		}
		if j.Outcome == nil {
			return ErrRecoveryRequired
		}
		if e := m.publish(ctx, *j.Outcome); e != nil {
			return e
		}
		if e := m.check("activity-published"); e != nil {
			return e
		}
	}
	if e := m.cleanupRecovery(dir); e != nil {
		return e
	}
	if e := os.Remove(m.journalPath()); e != nil {
		return e
	}
	return syncDir(m.root)
}

// Only called after the durable decision makes the recovery copy unnecessary.
// Remove and sync entries individually so cleanup can be interrupted and resumed
// without relying on recovery material that may already be partly removed.
func (m *Manager) cleanupRecovery(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return engineFailure(FailureRecoveryRequired, "invalid recovery directory")
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		if err = syncDir(dir); err != nil {
			return err
		}
		if err = m.check("recovery-entry-removed"); err != nil {
			return err
		}
	}
	if err = os.Remove(dir); err != nil {
		return err
	}
	return syncDir(m.root)
}
func mVerifyRecovery(dir string) error {
	b, e := os.ReadFile(filepath.Join(dir, "recovery.json"))
	if e != nil {
		return e
	}
	var want []payload
	if e = json.Unmarshal(b, &want); e != nil {
		return e
	}
	got, e := inventory(dir)
	if e != nil {
		return e
	}
	lookup := map[string]payload{}
	for _, v := range got {
		lookup[v.Name] = v
	}
	for _, v := range want {
		if !safeRelative(v.Name) || v.Name == "recovery.json" {
			return engineFailure(FailureRecoveryRequired, "invalid recovery inventory")
		}
		if lookup[v.Name] != v {
			return engineFailure(FailureRecoveryRequired, "recovery material failed verification")
		}
		delete(lookup, v.Name)
	}
	if len(lookup) != 1 || lookup["recovery.json"].Name == "" {
		return engineFailure(FailureRecoveryRequired, "recovery inventory incomplete")
	}
	required := map[string]bool{"database.dump": false, "data.sql": false, "pre-data.sql": false, "post-data.sql": false}
	for _, v := range want {
		if _, ok := required[v.Name]; ok {
			required[v.Name] = true
		}
	}
	for _, present := range required {
		if !present {
			return engineFailure(FailureRecoveryRequired, "required recovery payload missing")
		}
	}
	return nil
}
func (m *Manager) replaceFiles(src string) error {
	// Source is never moved: repeated rollback always has a complete durable copy.
	if e := privateDir(m.cfg.UploadDir); e != nil {
		return e
	}
	entries, e := os.ReadDir(m.cfg.UploadDir)
	if e != nil {
		return e
	}
	for _, v := range entries {
		if e = os.RemoveAll(filepath.Join(m.cfg.UploadDir, v.Name())); e != nil {
			return e
		}
		if e = syncDir(m.cfg.UploadDir); e != nil {
			return e
		}
		if e = m.check("files-entry-removed"); e != nil {
			return e
		}
	}
	if e = copyTree(m.cfg.UploadDir, src); e != nil {
		return e
	}
	return syncDir(m.cfg.UploadDir)
}
func (m *Manager) newJob(actor int64, kind string) (*job, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.cfg.RecoveryOnly || actor <= 0 {
		return nil, nil, ErrInvalid
	}
	unlock, e := m.cfg.Control.OperationLock()
	if e != nil {
		return nil, nil, e
	}
	if e = m.requireNoJournal(); e != nil {
		unlock()
		return nil, nil, e
	}
	m.expireLocked()
	if len(m.jobs) >= 32 {
		unlock()
		return nil, nil, engineFailure(FailureResourceLimit, "too many pending transfers")
	}
	var b [32]byte
	if _, e = rand.Read(b[:]); e != nil {
		unlock()
		return nil, nil, e
	}
	id := hex.EncodeToString(b[:])
	dir := filepath.Join(m.root, id)
	if e = os.Mkdir(dir, 0700); e != nil {
		unlock()
		return nil, nil, e
	}
	lease, e := os.OpenFile(filepath.Join(dir, "lease.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		unlock()
		_ = os.RemoveAll(dir)
		return nil, nil, e
	}
	if e = syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = lease.Close()
		unlock()
		_ = os.RemoveAll(dir)
		return nil, nil, e
	}
	j := &job{Operation: Operation{ID: id, Kind: kind, Phase: "queued", ExpiresAt: time.Now().Add(7 * time.Hour)}, actor: actor, dir: dir, active: true, lease: lease}
	m.jobs[id] = j
	m.wg.Add(1)
	return j, unlock, nil
}
func (m *Manager) expireLocked() {
	for id, j := range m.jobs {
		if !j.active && j.readers == 0 && time.Now().After(j.ExpiresAt) {
			_ = os.RemoveAll(j.dir)
			if j.lease != nil {
				_ = j.lease.Close()
				j.lease = nil
			}
			delete(m.jobs, id)
		}
	}
}
func (m *Manager) phase(j *job, p string) { m.mu.Lock(); j.Phase = p; m.mu.Unlock() }
func (m *Manager) run(j *job, unlock func(), fn func(context.Context) error) {
	go func() {
		defer m.wg.Done()
		defer unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		e := fn(ctx)
		m.mu.Lock()
		phase := j.Phase
		m.mu.Unlock()
		var failure Failure
		if e != nil {
			failure = m.reportFailure(e, phase)
		}
		if j.Kind != "restore" {
			if auditErr := m.finishActivity(j, failure); auditErr != nil {
				e = classified(FailureRecoveryRequired, errors.Join(auditErr, e))
				failure = m.reportFailure(e, phase)
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		j.active = false
		if e != nil {
			j.Phase = "failed"
			if j.committed {
				j.Phase = "complete"
			}
			j.Failure = failure
			j.Error = failure.Message
		} else if j.Kind == "upload" {
			j.Phase = "ready"
		} else {
			j.Phase = "complete"
		}
		j.ExpiresAt = time.Now().Add(time.Hour)
		time.AfterFunc(time.Hour, func() { m.mu.Lock(); defer m.mu.Unlock(); m.expireLocked() })
		if j.Phase == "failed" || j.Kind == "restore" {
			_ = os.RemoveAll(j.dir)
			if j.lease != nil {
				_ = j.lease.Close()
				j.lease = nil
			}
		}
	}()
}
func (m *Manager) Status(actor int64, id string) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	j := m.jobs[id]
	if j == nil || j.actor != actor {
		return Operation{}, ErrNotFound
	}
	return j.Operation, nil
}

// StatusCapability accepts the 256-bit operation ID as a bearer status-only
// capability, usable after restore invalidates the initiating session.
func (m *Manager) StatusCapability(id string) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	j := m.jobs[id]
	if j == nil {
		return Operation{}, ErrNotFound
	}
	return j.Operation, nil
}
func (m *Manager) StartBackup(actor int64, source, password string) (Operation, error) {
	return m.StartBackupFor(ActivityActor{ID: actor}, source, password)
}
func (m *Manager) StartBackupFor(actor ActivityActor, source, password string) (Operation, error) {
	if !m.validActor(actor) {
		return Operation{}, ErrInvalid
	}
	if password == "" || len(password) > 1024 || source == "" || len(source) > 256 {
		return Operation{}, ErrInvalid
	}
	j, unlock, e := m.newJob(actor.ID, "backup")
	if e != nil {
		return Operation{}, e
	}
	if e = m.beginActivity(j, actor, "backup_failed"); e != nil {
		m.cancelJob(j, unlock)
		return Operation{}, e
	}
	m.run(j, unlock, func(ctx context.Context) error {
		m.phase(j, "preflight")
		if e := m.preflight(ctx); e != nil {
			return e
		}
		m.phase(j, "capturing")
		release, e := m.cfg.Control.Maintenance(ctx)
		if e != nil {
			return e
		}
		defer release()
		man, e := m.capture(ctx, j.dir, source)
		clearErr := m.clear()
		if clearErr != nil {
			return classified(FailureRecoveryRequired, errors.Join(e, clearErr))
		}
		if e != nil {
			return e
		}
		m.phase(j, "encrypting")
		if e = seal(j.dir, password, man); e != nil {
			return e
		}
		password = ""
		m.mu.Lock()
		j.SourceName = source
		j.CapturedAt = man.Captured
		j.Version = man.Version
		j.BuildKind = man.BuildKind
		j.Commit = man.Commit
		m.mu.Unlock()
		// Keep only encrypted download bytes after successful capture.
		entries, e := os.ReadDir(j.dir)
		if e != nil {
			return e
		}
		for _, v := range entries {
			if v.Name() != "archive.age" && v.Name() != "lease.lock" {
				if e = os.RemoveAll(filepath.Join(j.dir, v.Name())); e != nil {
					return e
				}
			}
		}
		return nil
	})
	return m.Status(actor.ID, j.ID)
}
func (m *Manager) capture(ctx context.Context, dir, source string) (manifest, error) {
	man := manifest{Format: 1, Source: source, BuildKind: m.cfg.BuildKind, Version: m.cfg.Version, Commit: m.cfg.Commit, Captured: time.Now().UTC()}
	var e error
	man.Paths, e = m.mappings(ctx)
	if e != nil {
		return man, e
	}
	if e = m.dump(ctx, dir); e != nil {
		return man, e
	}
	man.Schema, e = m.prepare(ctx, dir)
	if e != nil {
		return man, e
	}
	if e = copyTree(filepath.Join(dir, "uploads"), m.cfg.UploadDir); e != nil {
		return man, e
	}
	items, e := inventory(dir)
	if e != nil {
		return man, e
	}
	for _, v := range items {
		if v.Name == "database.dump" || len(v.Name) > 8 && v.Name[:8] == "uploads/" {
			man.Payloads = append(man.Payloads, v)
		}
	}
	return man, syncDir(dir)
}

// StageUpload streams input synchronously to bounded disk storage; authenticated
// decryption and full validation continue on a job context after input EOF.
func (m *Manager) StageUpload(ctx context.Context, actor int64, r io.Reader, password string) (Operation, error) {
	return m.StageUploadFor(ctx, ActivityActor{ID: actor}, r, password)
}
func (m *Manager) StageUploadFor(ctx context.Context, actor ActivityActor, r io.Reader, password string) (Operation, error) {
	if !m.validActor(actor) {
		return Operation{}, ErrInvalid
	}
	if m.cfg.RecoveryOnly {
		return Operation{}, ErrInvalid
	}
	if password == "" || len(password) > 1024 {
		return Operation{}, ErrInvalid
	}
	if e := space(m.root, 256<<20); e != nil {
		return Operation{}, e
	}
	j, unlock, e := m.newJob(actor.ID, "upload")
	if e != nil {
		return Operation{}, e
	}
	if e = m.beginActivity(j, actor, "backup_validation_failed"); e != nil {
		m.cancelJob(j, unlock)
		return Operation{}, e
	}
	f, e := os.OpenFile(filepath.Join(j.dir, "archive.age"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e == nil {
		_, e = io.Copy(&capacityWriter{dir: m.root, w: f}, &contextReader{ctx: ctx, r: io.LimitReader(r, maxBytes+maxManifest+1)})
		if e == nil {
			var s os.FileInfo
			s, e = f.Stat()
			if e == nil && s.Size() > maxBytes+maxManifest {
				e = engineFailure(FailureResourceLimit, "upload exceeds resource limit")
			}
		}
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
	}
	if e != nil {
		failure := m.reportFailure(e, "uploading")
		if auditErr := m.finishActivity(j, failure); auditErr != nil {
			e = classified(FailureRecoveryRequired, errors.Join(auditErr, e))
			m.reportFailure(e, "uploading")
		}
		unlock()
		m.mu.Lock()
		delete(m.jobs, j.ID)
		m.mu.Unlock()
		m.wg.Done()
		_ = os.RemoveAll(j.dir)
		if j.lease != nil {
			_ = j.lease.Close()
		}
		return Operation{}, e
	}
	m.run(j, unlock, func(ctx context.Context) error {
		m.phase(j, "preflight")
		if e := m.preflight(ctx); e != nil {
			return e
		}
		m.phase(j, "validating")
		man, e := unseal(j.dir, password)
		password = ""
		if e != nil {
			return e
		}
		if man.BuildKind == BuildRelease && m.cfg.BuildKind == BuildRelease && (man.Version != m.cfg.Version || man.Commit != m.cfg.Commit) {
			return engineFailure(FailureReleaseMismatch, "release mismatch")
		}
		hash, e := m.prepare(ctx, j.dir)
		if e != nil {
			return e
		}
		if hash != man.Schema {
			return engineFailure(FailureSchemaMismatch, "schema fingerprint mismatch")
		}
		trusted := filepath.Join(j.dir, "trusted-validation")
		local, e := m.localSchema(ctx, trusted)
		if e != nil {
			return e
		}
		if local != hash {
			return engineFailure(FailureSchemaMismatch, "uploaded schema differs from trusted destination schema")
		}
		if e = os.RemoveAll(trusted); e != nil {
			return e
		}
		if e = validateReferences(ctx, j.dir, man); e != nil {
			return e
		}
		if e = m.validateStructure(ctx, j.dir); e != nil {
			return e
		}
		digest, e := digestFile(filepath.Join(j.dir, "archive.age"))
		if e != nil {
			return e
		}
		m.mu.Lock()
		j.manifest = man
		j.SourceName = man.Source
		j.BuildKind = man.BuildKind
		j.Version = man.Version
		j.Commit = man.Commit
		j.CapturedAt = man.Captured
		j.ArchiveDigest = digest
		m.mu.Unlock()
		return nil
	})
	return m.Status(actor.ID, j.ID)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func (m *Manager) StartRestore(ctx context.Context, actor int64, id, destination, confirmation string) (Operation, error) {
	return m.StartRestoreFor(ctx, ActivityActor{ID: actor}, id, destination, confirmation)
}
func (m *Manager) StartRestoreFor(ctx context.Context, actor ActivityActor, id, destination, confirmation string) (Operation, error) {
	if !m.validActor(actor) {
		return Operation{}, ErrInvalid
	}
	if m.cfg.RecoveryOnly {
		return Operation{}, ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		return Operation{}, e
	}
	if destination == "" || destination != confirmation || m.cfg.ResetConnections == nil {
		return Operation{}, ErrInvalid
	}
	m.mu.Lock()
	j := m.jobs[id]
	if m.closed || j == nil || j.actor != actor.ID || j.Phase != "ready" || time.Now().After(j.ExpiresAt) {
		m.mu.Unlock()
		return Operation{}, ErrInvalid
	}
	unlock, e := m.cfg.Control.OperationLock()
	if e != nil {
		m.mu.Unlock()
		return Operation{}, e
	}
	if e = m.requireNoJournal(); e != nil {
		unlock()
		m.mu.Unlock()
		return Operation{}, e
	}
	j.active = true
	j.activityActor = actor
	j.Kind = "restore"
	j.Phase = "queued"
	m.wg.Add(1)
	m.mu.Unlock()
	m.run(j, unlock, func(ctx context.Context) error { return m.restore(ctx, j) })
	return m.Status(actor.ID, id)
}
func (m *Manager) restore(ctx context.Context, j *job) error {
	recovery := j.ID + "-recovery"
	dir := filepath.Join(m.root, recovery)
	state := journal{Phase: "capturing", Recovery: recovery, EmailDisabled: m.cfg.Control.EmailDisabled()}
	if m.cfg.ActivitySink != nil {
		start, err := m.activity(j.activityActor, "restore_started")
		if err != nil {
			return err
		}
		outcome, err := m.activity(j.activityActor, "restore_failed")
		if err != nil {
			return err
		}
		setActivityFailure(&outcome, interruptedActivityFailure("recovering"))
		state.Start, state.Outcome = &start, &outcome
	}
	if e := m.writeJournal(state); e != nil {
		return e
	}
	release, e := m.cfg.Control.Maintenance(ctx)
	if e != nil {
		// The accepted attempt has durable evidence even when draining normal
		// admissions fails. Startup will publish its interrupted outcome.
		return classified(FailureRecoveryRequired, e)
	}
	defer release()
	workErr := func() error {
		if state.Start != nil {
			if e := m.publish(ctx, *state.Start); e != nil {
				return e
			}
		}
		m.phase(j, "preflight")
		if e := m.preflight(ctx); e != nil {
			return e
		}
		digest, e := digestFile(filepath.Join(j.dir, "archive.age"))
		if e != nil || digest != j.ArchiveDigest {
			return engineFailure(FailureArchiveIntegrity, "staged archive changed")
		}
		m.phase(j, "recovery-copy")
		if e := privateDir(dir); e != nil {
			return e
		}
		man, e := m.capture(ctx, dir, "recovery")
		if e != nil {
			return e
		}
		if man.Schema != j.manifest.Schema {
			return engineFailure(FailureSchemaMismatch, "uploaded schema differs from trusted destination schema")
		}
		items, e := inventory(dir)
		if e != nil {
			return e
		}
		if e = atomicJSON(filepath.Join(dir, "recovery.json"), items); e != nil {
			return e
		}
		if e = syncDir(m.root); e != nil {
			return e
		}
		if e = mVerifyRecovery(dir); e != nil {
			return e
		}
		var size int64
		for _, p := range j.manifest.Payloads {
			size += p.Size
		}
		if e = space(m.cfg.UploadDir, size); e != nil {
			return e
		}
		state.Phase = "replacing"
		if e = m.writeJournal(state); e != nil {
			return e
		}
		if e = m.check("replacing-durable"); e != nil {
			return e
		}
		m.phase(j, "replacing-database")
		if e = m.apply(ctx, dir, j.dir); e != nil {
			return e
		}
		m.phase(j, "replacing-files")
		if e = m.replaceFiles(filepath.Join(j.dir, "uploads")); e != nil {
			return e
		}
		m.phase(j, "protecting-instance")
		if e = m.protect(ctx, j.manifest.Paths); e != nil {
			return e
		}
		if e = m.cfg.Control.DisableEmail(); e != nil {
			return e
		}
		if e = m.check("protections-applied"); e != nil {
			return e
		}
		m.cfg.ResetConnections()
		m.phase(j, "committing")
		state.Phase = "committed"
		if state.Outcome != nil {
			state.Outcome.Action = "restore_completed"
			state.Outcome.OccurredAt = time.Now().UTC()
			setActivityFailure(state.Outcome, Failure{})
		}
		if e = m.writeJournal(state); e != nil {
			return e
		}
		j.committed = true
		return m.check("committed-durable")
	}()
	if workErr != nil && state.Phase == "committed" {
		// An unsuccessful commit fsync has an ambiguous durable outcome. Retain
		// all evidence and let startup recovery inspect the surviving journal.
		return classified(FailureRecoveryRequired, workErr)
	}
	// Use a fresh context even when the job timed out. The on-disk journal, not
	// an in-memory phase, is authoritative after an ambiguous fsync failure.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	b, e := os.ReadFile(m.journalPath())
	if e != nil {
		return classified(FailureRecoveryRequired, errors.Join(workErr, e))
	}
	if e = json.Unmarshal(b, &state); e != nil {
		return classified(FailureRecoveryRequired, errors.Join(workErr, e))
	}
	m.mu.Lock()
	failedPhase := j.Phase
	m.mu.Unlock()
	if workErr != nil {
		failure := m.reportFailure(workErr, failedPhase)
		workErr = &reportedFailure{error: workErr, failure: failure}
		if state.Outcome != nil {
			setActivityFailure(state.Outcome, failure)
			state.Outcome.OccurredAt = time.Now().UTC()
			if e = m.writeJournal(state); e != nil {
				return classified(FailureRecoveryRequired, e)
			}
		}
		m.phase(j, "rolling-back")
	}
	if e = m.resolve(recoveryCtx, state); e != nil {
		return classified(FailureRecoveryRequired, errors.Join(workErr, e))
	}
	if e = m.clear(); e != nil {
		return classified(FailureRecoveryRequired, errors.Join(workErr, e))
	}
	if workErr != nil {
		m.phase(j, failedPhase)
	}
	return workErr
}

type download struct {
	*os.File
	once    sync.Once
	release func()
}

func (d *download) Close() error { e := d.File.Close(); d.once.Do(d.release); return e }
func (m *Manager) OpenDownload(actor int64, id string) (io.ReadCloser, string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	j := m.jobs[id]
	if j == nil || j.actor != actor || j.Kind != "backup" || j.Phase != "complete" || time.Now().After(j.ExpiresAt) {
		return nil, "", 0, ErrNotFound
	}
	f, e := os.Open(filepath.Join(j.dir, "archive.age"))
	if e != nil {
		return nil, "", 0, e
	}
	s, e := f.Stat()
	if e != nil {
		_ = f.Close()
		return nil, "", 0, e
	}
	j.readers++
	return &download{File: f, release: func() { m.mu.Lock(); defer m.mu.Unlock(); j.readers--; m.expireLocked() }}, "freefsm-" + j.ID + ".age", s.Size(), nil
}
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.lease != nil {
			_ = j.lease.Close()
			j.lease = nil
		}
	}
}

func cleanupAbandoned(dir string) error {
	f, e := os.OpenFile(filepath.Join(dir, "lease.lock"), os.O_RDWR, 0600)
	if errors.Is(e, os.ErrNotExist) {
		return os.RemoveAll(dir)
	}
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		if errors.Is(e, syscall.EWOULDBLOCK) {
			return nil
		}
		return e
	}
	return os.RemoveAll(dir)
}

// RefreshConnections resets this process's application pools when another
// process replaced the schema. Call after admission, before using any pool, in
// each request and background attempt. ResetConnections must cover every pool.
func (m *Manager) RefreshConnections() error {
	m.poolMu.Lock()
	defer m.poolMu.Unlock()
	b, e := os.ReadFile(filepath.Join(m.root, "schema-generation.json"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var generation string
	if e = json.Unmarshal(b, &generation); e != nil {
		return e
	}
	if generation != m.generation {
		if m.cfg.ResetConnections == nil {
			return engineFailure(FailureRecoveryRequired, "application pool reset callback required")
		}
		m.cfg.ResetConnections()
		m.generation = generation
	}
	return nil
}
func (m *Manager) advanceGeneration() error {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return e
	}
	return atomicJSON(filepath.Join(m.root, "schema-generation.json"), hex.EncodeToString(b[:]))
}
