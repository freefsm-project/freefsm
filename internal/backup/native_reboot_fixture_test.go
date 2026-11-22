package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
)

// TestNativeRebootFixture is a deliberately manual, guest-only fixture interface.
// It neither reboots the machine nor starts/recovers the production service.
// Run this test ONLY in a fresh disposable Linux guest with local PostgreSQL
// trust authentication for the generated unprivileged crash_* roles.
//
// FREEFSM_NATIVE_REBOOT_ROOT: absolute, persistent, initially nonexistent directory.
// FREEFSM_NATIVE_REBOOT_ACTION: setup, run, assert, enable, assert-enabled.
// FREEFSM_NATIVE_REBOOT_CASE (setup only): protections-applied,
// committed-durable, recovery-entry-removed, complete.
// Setup additionally requires FREEFSM_BACKUP_TEST_ADMIN_URL; no other action does.
// Setup emits child.json (DSN/paths) and native.env (production config). Start the
// native service with that environment on the NEXT boot, not during run. Run
// blocks at ready.json for crash cases; use -test.timeout=0. For complete it exits.
// Reboot the actual guest after ready.json appears, then wait for the native
// service to become healthy before assert. Assert never calls Recover/BeginStartup.
// Repeat reboot/assert to check idempotence. For complete, follow assert with
// enable, another actual reboot/native startup, then assert-enabled.
// Keep the root private; all resources persist until the disposable guest is
// discarded. A changed Linux boot ID proves an OS boot, not physical power loss.
func TestNativeRebootFixture(t *testing.T) {
	root := os.Getenv("FREEFSM_NATIVE_REBOOT_ROOT")
	if root == "" {
		t.Skip("manual disposable-guest reboot fixture")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		t.Fatal("fixture root must be a clean absolute non-root path")
	}
	action := os.Getenv("FREEFSM_NATIVE_REBOOT_ACTION")
	if action == "setup" {
		nativeRebootSetup(t, root)
		return
	}
	var spec crashChildSpec
	nativeReadJSON(t, filepath.Join(root, "child.json"), &spec)
	if spec.StateDir != filepath.Join(root, "destination", "state") || spec.UploadDir != filepath.Join(root, "destination", "uploads") || spec.Ready != filepath.Join(root, "ready.json") {
		t.Fatal("fixture paths do not match this root")
	}
	if action == "run" {
		if _, err := os.Lstat(filepath.Join(root, "run-boot.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("run is single-use; create a fresh case root")
		}
		nativeWriteJSON(t, filepath.Join(root, "run-boot.json"), nativeBootID(t))
		t.Setenv("FREEFSM_BACKUP_CRASH_CHILD_SPEC", filepath.Join(root, "child.json"))
		TestBackupCrashChild(t)
		return
	}
	if action != "assert" && action != "enable" && action != "assert-enabled" {
		t.Fatal("unknown fixture action")
	}
	var before, reached string
	nativeReadJSON(t, filepath.Join(root, "run-boot.json"), &before)
	nativeReadJSON(t, spec.Ready, &reached)
	want := spec.Checkpoint
	if spec.Mode == "restore-complete" {
		want = "complete"
	}
	if reached != want || nativeBootID(t) == before {
		t.Fatal("requires the selected barrier and a different guest boot ID")
	}
	control, err := instancecontrol.New(spec.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{DSN: spec.DSN, UploadDir: spec.UploadDir, StateDir: spec.StateDir, Version: "v1.2.3", Commit: "abcdef1234567", Control: control, ResetConnections: func() {}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if _, err = os.Lstat(m.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("native startup has not resolved the restore journal")
	}
	_, release, err := control.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if action == "assert-enabled" {
		var enabledBoot, expected string
		nativeReadJSON(t, filepath.Join(root, "enable-boot.json"), &enabledBoot)
		nativeReadJSON(t, filepath.Join(root, "enabled-snapshot.json"), &expected)
		if nativeBootID(t) == enabledBoot || nativePolicySnapshot(t, m) != expected || crashEmailDisabled(t, m) {
			t.Fatal("sealed enable did not survive a further guest reboot unchanged")
		}
		assertCrashState(t, m, "source")
	} else if action == "enable" {
		if spec.Mode != "restore-complete" {
			t.Fatal("enable is only for the complete case")
		}
		assertCommittedCrashState(t, m)
		nativeEnableEmail(t, m)
		nativeWriteJSON(t, filepath.Join(root, "enabled-snapshot.json"), nativePolicySnapshot(t, m))
		nativeWriteJSON(t, filepath.Join(root, "enable-boot.json"), nativeBootID(t))
	} else if spec.Checkpoint == "protections-applied" {
		assertCrashState(t, m, "destination")
		var expected string
		nativeReadJSON(t, filepath.Join(root, "destination-snapshot.json"), &expected)
		if nativePolicySnapshot(t, m) != expected {
			t.Fatal("rollback changed prior policy, sessions, audit, effects or receipts")
		}
	} else {
		assertCommittedCrashState(t, m)
		if err := nativeRecordCommitted(filepath.Join(root, "committed-snapshot.json"), nativeBootID(t), nativePolicySnapshot(t, m)); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("native fixture %s passed: pre-restore boot=%s current boot=%s", action, before, nativeBootID(t))
}

// The first recovered boot establishes the exact committed baseline. Each later
// assertion must observe another boot without changing that baseline. Keep this
// separate from enable's intentional mutation and its own persisted expectation.
func nativeRecordCommitted(path, boot, snapshot string) error {
	var previous struct{ Boot, Snapshot string }
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &previous); err != nil {
			return err
		}
		if previous.Boot == "" || previous.Snapshot == "" {
			return errors.New("incomplete committed reboot baseline")
		}
		if previous.Boot == boot {
			return errors.New("committed idempotence assertion requires a further guest reboot")
		}
		if previous.Snapshot != snapshot {
			return errors.New("committed policy, sessions, audit, effects or receipts changed across reboot")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous.Boot, previous.Snapshot = boot, snapshot
	return atomicJSON(path, previous)
}

func TestNativeCommittedRebootBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "committed-snapshot.json")
	const snapshot = `{"epoch":"original","revision":2,"revoked_at":"original"}`
	if err := nativeRecordCommitted(path, "boot-1", snapshot); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("baseline must be private: %v %v", info, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct{ boot, snapshot string }{
		{"boot-1", snapshot}, // Re-running assert is not another reboot.
		{"boot-2", `{"epoch":"reapplied","revision":3,"revoked_at":"later"}`},
	} {
		if err := nativeRecordCommitted(path, attempt.boot, attempt.snapshot); err == nil {
			t.Fatal("invalid reboot evidence accepted")
		}
		current, err := os.ReadFile(path)
		if err != nil || string(current) != string(original) {
			t.Fatal("rejected assertion replaced the baseline")
		}
	}
	for _, boot := range []string{"boot-2", "boot-3"} {
		if err := nativeRecordCommitted(path, boot, snapshot); err != nil {
			t.Fatal(err)
		}
		if err := nativeRecordCommitted(path, boot, snapshot); err == nil {
			t.Fatal("last successful assertion's boot was not retained")
		}
	}
}

func nativeRebootSetup(t *testing.T, root string) {
	t.Helper()
	if os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL") == "" {
		t.Fatal("setup requires a disposable guest PostgreSQL admin URL")
	}
	point := os.Getenv("FREEFSM_NATIVE_REBOOT_CASE")
	switch point {
	case "protections-applied", "committed-durable", "recovery-entry-removed", "complete":
	default:
		t.Fatal("select one of the four documented native reboot cases")
	}
	f := newCrashFixture(t)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("setup requires a new directory: ", err)
	}
	src := f.manager(t, "source")
	seedCrashSessions(t, src, "source")
	archive := createCrashArchive(t, src)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archive = filepath.Join(root, "archive.age")
	if err = nativeWriteFile(archive, data); err != nil {
		t.Fatal(err)
	}
	dst := f.managerAt(t, "destination", filepath.Join(root, "destination"), true)
	seedCrashSessions(t, dst, "destination")
	nativeWriteJSON(t, filepath.Join(root, "destination-snapshot.json"), nativePolicySnapshot(t, dst))
	spec := crashChildSpec{DSN: dst.cfg.DSN, UploadDir: dst.cfg.UploadDir, StateDir: dst.cfg.StateDir, Archive: archive, Mode: "restore", Checkpoint: point, Ready: filepath.Join(root, "ready.json")}
	if point == "complete" {
		spec.Mode, spec.Checkpoint = "restore-complete", ""
	}
	nativeWriteJSON(t, filepath.Join(root, "child.json"), spec)
	// Production config is deliberately separate from the test-only failpoint.
	c, err := dst.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	cfg := c.Config()
	env := []string{"FREEFSM_DB_HOST=" + cfg.Host, "FREEFSM_DB_PORT=" + strconv.Itoa(int(cfg.Port)), "FREEFSM_DB_NAME=" + cfg.Database, "FREEFSM_DB_USER=" + cfg.User, "FREEFSM_DB_PASSWORD=", "FREEFSM_STATE_DIR=" + dst.cfg.StateDir, "FREEFSM_UPLOAD_DIR=" + dst.cfg.UploadDir, "FREEFSM_ADDR=127.0.0.1:8080", "FREEFSM_SESSION_SECRET=" + uuid.NewString(), "FREEFSM_SETUP_TOKEN=" + uuid.NewString()}
	if err = nativeWriteFile(filepath.Join(root, "native.env"), []byte(strings.Join(env, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
}

func nativeWriteFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func nativeBootID(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(b))
	if _, err = uuid.Parse(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func nativeWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := atomicJSON(path, v); err != nil {
		t.Fatal(err)
	}
}

func nativeReadJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func nativePolicySnapshot(t *testing.T, m *Manager) string {
	t.Helper()
	c, err := m.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var snapshot string
	err = c.QueryRow(context.Background(), `SELECT jsonb_build_array(
		(SELECT to_jsonb(p) FROM restore_email_policy p),
		(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM sessions s),
		(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a),
		(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM online_save_actions a),
		(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM online_save_effects e))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func nativeEnableEmail(t *testing.T, m *Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Connect(ctx, m.cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var user int64
	if err = db.Pool.QueryRow(ctx, `SELECT u.id FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE u.email='source@crash.invalid' AND r.protected`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	// Model a fresh login after restore invalidated the archived sessions.
	token := uuid.NewString()
	if _, err = db.Pool.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES($1,$2,'web',clock_timestamp()+interval '1 day')`, user, token); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user, SessionHash: token, Channel: "web"}
	service := authorization.New(db.Pool)
	before := nativePolicySnapshot(t, m)
	if err = service.EnableEmail(ctx, actor); err == nil || nativePolicySnapshot(t, m) != before {
		t.Fatal("unsealed enable accepted or changed persistent state")
	}
	p, err := emailpolicy.Read(ctx, db.Pool, false)
	if err != nil || p.Enabled {
		t.Fatal("expected disabled current policy: ", err)
	}
	store := saveaction.Store{Pool: db.Pool}
	stale := saveaction.Action{UserID: user, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: p.Epoch + "/0", IntentHash: "native-reboot-stale-enable", Atomic: true, NoChange: true}
	if ok, err := store.AdmitExecution(ctx, stale, stale.IntentHash, "POST", stale.Path, "/settings/mail"); err != nil || !ok {
		t.Fatalf("stale admission: %v %v", ok, err)
	}
	before = nativePolicySnapshot(t, m)
	if err = service.EnableEmail(saveaction.WithAction(ctx, stale), actor); !errors.Is(err, authorization.ErrConflict) || nativePolicySnapshot(t, m) != before {
		t.Fatal("stale review accepted or changed persistent state: ", err)
	}
	a := saveaction.Action{UserID: user, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: p.Review(), IntentHash: "native-reboot-reviewed-enable", Atomic: true, NoChange: true}
	if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
		t.Fatalf("sealed admission: %v %v", ok, err)
	}
	if err = service.EnableEmail(saveaction.WithAction(ctx, a), actor); err != nil {
		t.Fatal(err)
	}
	after, err := emailpolicy.Read(ctx, db.Pool, false)
	if err != nil || !after.Enabled || after.Epoch != p.Epoch || after.Revision != p.Revision+1 || after.LastEnableOperation == nil || *after.LastEnableOperation != a.ID {
		t.Fatal("sealed enable policy invariant failed: ", err)
	}
	var audit, effects int
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text),(SELECT count(*) FROM online_save_effects WHERE operation_id=$1::uuid)`, a.ID).Scan(&audit, &effects); err != nil || audit != 1 || effects != 1 {
		t.Fatalf("sealed enable audit/effects=%d/%d: %v", audit, effects, err)
	}
	receipt, err := store.Receipt(ctx, a)
	if err != nil || receipt.Disposition != "saved" {
		t.Fatal("sealed enable receipt not saved: ", err)
	}
	before = nativePolicySnapshot(t, m)
	if err = service.EnableEmail(saveaction.WithAction(ctx, a), actor); err == nil || nativePolicySnapshot(t, m) != before {
		t.Fatal("duplicate enable accepted or changed persistent state")
	}
}
