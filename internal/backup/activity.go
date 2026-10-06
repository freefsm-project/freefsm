package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// There is at most one pending ordinary outcome: the operation lock serializes
// producers and requireNoJournal blocks further work until publication succeeds.
// It lives outside disposable job directories and is drained before replacement.
type pendingActivity struct {
	Destination string
	Event       ActivityEvent
}

func (m *Manager) pendingPath() string { return filepath.Join(m.root, "pending-activity.json") }

func (m *Manager) validActor(actor ActivityActor) bool {
	return actor.ID > 0 && (m.cfg.ActivitySink == nil || (strings.TrimSpace(actor.Name) != "" && len(actor.Name) <= 1024 && len(actor.CompanyName) <= 1024))
}

func (m *Manager) activity(actor ActivityActor, action string) (ActivityEvent, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return ActivityEvent{}, err
	}
	return ActivityEvent{Key: hex.EncodeToString(key[:]), Action: action, Actor: actor, OccurredAt: time.Now().UTC()}, nil
}

func interruptedActivityFailure(phase string) Failure {
	// The eventual crash/interruption has not happened yet. This durable fallback
	// is not an observed diagnostic and must not claim a correlating log record.
	return Failure{Category: FailureRecoveryRequired, Phase: phase, Message: failureMessages[FailureRecoveryRequired]}
}

// Activity refers to the diagnostic already persisted/emitted by the engine;
// generating another diagnostic here would create an unresolvable correlator.
func setActivityFailure(event *ActivityEvent, failure Failure) {
	event.FailureCategory, event.FailurePhase, event.FailureMessage, event.DiagnosticID = string(failure.Category), failure.Phase, failure.Message, failure.DiagnosticID
}

func (m *Manager) publish(ctx context.Context, event ActivityEvent) error {
	if m.cfg.ActivitySink == nil || m.cfg.ActivitySink(ctx, event) != nil {
		// A sink can return SQL/connection errors containing secrets. Retain only
		// an actionable fixed category; durable evidence remains for startup.
		return engineFailure(FailureRecoveryRequired, "backup activity publication failed; restore database availability and retry startup recovery")
	}
	return nil
}

func (m *Manager) savePending(event ActivityEvent) error {
	if err := atomicJSON(m.pendingPath(), pendingActivity{m.destinationIdentity(), event}); err != nil {
		return classified(FailureRecoveryRequired, err)
	}
	return nil
}

func (m *Manager) replayPending(ctx context.Context) error {
	b, err := os.ReadFile(m.pendingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingActivity
	if err = json.Unmarshal(b, &pending); err != nil {
		return err
	}
	if pending.Destination != m.destinationIdentity() {
		return ErrRecoveryRequired
	}
	if err = m.publish(ctx, pending.Event); err != nil {
		return err
	}
	if err = os.Remove(m.pendingPath()); err != nil {
		return err
	}
	return syncDir(m.root)
}

func (m *Manager) beginActivity(j *job, actor ActivityActor, action string) error {
	j.activityActor = actor
	if m.cfg.ActivitySink == nil {
		return nil
	}
	event, err := m.activity(actor, action)
	if err != nil {
		return err
	}
	setActivityFailure(&event, interruptedActivityFailure("interrupted"))
	j.outcome = event
	return m.savePending(event)
}

func (m *Manager) finishActivity(j *job, failure Failure) error {
	if j.outcome.Key == "" {
		return nil
	}
	event := j.outcome
	event.OccurredAt = time.Now().UTC()
	setActivityFailure(&event, failure)
	if failure.Category == "" {
		if j.Kind == "backup" {
			event.Action = "backup_created"
		} else {
			event.Action = "backup_validated"
		}
	}
	if err := m.savePending(event); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return m.replayPending(ctx)
}

func (m *Manager) cancelJob(j *job, unlock func()) {
	m.mu.Lock()
	delete(m.jobs, j.ID)
	m.mu.Unlock()
	_ = os.RemoveAll(j.dir)
	_ = j.lease.Close()
	m.wg.Done()
	unlock()
}

// RecordDownload is called after OpenDownload and before the first stream byte.
// A failed insert denies this attempt without changing the backup operation.
func (m *Manager) RecordDownload(ctx context.Context, actor ActivityActor, id string) error {
	if !m.validActor(actor) {
		return ErrInvalid
	}
	m.mu.Lock()
	j := m.jobs[id]
	valid := j != nil && j.actor == actor.ID && j.Kind == "backup" && j.Phase == "complete" && j.readers > 0
	m.mu.Unlock()
	if !valid {
		return ErrNotFound
	}
	if m.cfg.ActivitySink == nil {
		return nil
	}
	event, err := m.activity(actor, "backup_download_started")
	if err != nil {
		return err
	}
	if err = m.publish(ctx, event); err != nil {
		m.reportFailure(err, "download-audit")
	}
	return err
}
