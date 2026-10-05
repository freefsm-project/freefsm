package backup

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
)

// This URL must point only to a disposable cluster: the test creates two unique
// databases and two NO-CREATEDB application roles, and removes them on exit.
func TestEncryptedCrossRoleRestoreAndRecovery(t *testing.T) {
	adminURL := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("disposable PostgreSQL admin URL required")
	}
	ctx := context.Background()
	admin, e := pgx.Connect(ctx, adminURL)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	var managers []*Manager
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("backup_%d_%d", time.Now().UnixNano(), i)
		ident := pgx.Identifier{name}.Sanitize()
		if _, e = admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOCREATEDB NOSUPERUSER"); e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'"); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)")
			_, _ = admin.Exec(ctx, "DROP ROLE "+ident)
		})
		cfg, e := pgx.ParseConfig(adminURL)
		if e != nil {
			t.Fatal(e)
		}
		dsn := fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", name, cfg.Host, cfg.Port, name)
		root := t.TempDir()
		control, e := instancecontrol.New(filepath.Join(root, "state"))
		if e != nil {
			t.Fatal(e)
		}
		m, e := New(Config{DSN: dsn, UploadDir: filepath.Join(root, "uploads"), StateDir: control.StateDir(), Version: "v1.2.3", Commit: "abcdef1234567", Control: control, ResetConnections: func() {}})
		if e != nil {
			t.Fatal(e)
		}
		managers = append(managers, m)
		if e = m.Recover(ctx); e != nil {
			t.Fatal(e)
		}
		if e = os.MkdirAll(m.cfg.UploadDir, 0700); e != nil {
			t.Fatal(e)
		}
		c, e := m.connect(ctx)
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.Exec(ctx, `CREATE TABLE files(id bigserial primary key,file_path text NOT NULL); CREATE TABLE company_settings(id bigserial primary key,invoice_logo_path text); CREATE TABLE sessions(id bigserial primary key,expires_at timestamptz,revoked_at timestamptz); CREATE TABLE evidence(id bigserial primary key,body text); CREATE FUNCTION immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable'; END $$; CREATE TRIGGER immutable BEFORE UPDATE OR DELETE ON evidence FOR EACH ROW EXECUTE FUNCTION immutable();`)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(m.cfg.UploadDir, "history.bin")
		if e = os.WriteFile(path, []byte(fmt.Sprintf("historical bytes %d", i)), 0600); e != nil {
			t.Fatal(e)
		}
		if i == 0 && os.Getenv("FREEFSM_BACKUP_TEST_GB") == "1" {
			f, e := os.Create(filepath.Join(m.cfg.UploadDir, "incompressible.bin"))
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.CopyN(f, rand.Reader, 1<<30)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.Close(); e != nil {
				t.Fatal(e)
			}
		}
		_, e = c.Exec(ctx, `INSERT INTO files(file_path) VALUES($1); INSERT INTO company_settings(invoice_logo_path) VALUES($1); INSERT INTO sessions(expires_at) VALUES(now()+interval '1 day'); INSERT INTO evidence(body) VALUES('retained immutable snapshot');`, path)
		if e != nil {
			t.Fatal(e)
		}
		_ = c.Close(ctx)
	}
	src, dst := managers[0], managers[1]
	for _, m := range managers {
		if e = m.Recover(ctx); e != nil {
			t.Fatal(e)
		}
	}
	op, e := src.StartBackup(1, "source", "long archive password")
	if e != nil {
		t.Fatal(e)
	}
	src.wg.Wait()
	op, e = src.Status(1, op.ID)
	if e != nil || op.Phase != "complete" {
		t.Fatalf("backup: %+v %v", op, e)
	}
	reader, _, archiveSize, e := src.OpenDownload(1, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("encrypted archive: %d bytes", archiveSize)
	if os.Getenv("FREEFSM_BACKUP_TEST_GB") == "1" && archiveSize < 1<<30 {
		t.Fatal("archive below scale target")
	}
	staged, e := dst.StageUpload(ctx, 2, reader, "long archive password")
	_ = reader.Close()
	if e != nil {
		t.Fatal(e)
	}
	dst.wg.Wait()
	staged, _ = dst.Status(2, staged.ID)
	if staged.Phase != "ready" {
		t.Fatalf("validation: %+v", staged)
	}
	if _, e = src.Status(999, op.ID); e == nil {
		t.Fatal("cross-actor status accepted")
	}
	if _, _, _, e = src.OpenDownload(999, op.ID); e == nil {
		t.Fatal("cross-actor download accepted")
	}
	if _, e = dst.StartRestore(ctx, 999, staged.ID, "destination", "destination"); e == nil {
		t.Fatal("cross-actor restore accepted")
	}
	if _, e = dst.StartRestore(ctx, 2, staged.ID, "destination", "wrong"); e == nil {
		t.Fatal("wrong confirmation accepted")
	}
	request, cancel := context.WithCancel(ctx)
	restored, e := dst.StartRestore(request, 2, staged.ID, "destination", "destination")
	cancel()
	if e != nil {
		t.Fatal(e)
	}
	dst.wg.Wait()
	restored, e = dst.StatusCapability(restored.ID)
	if e != nil || restored.Phase != "complete" {
		t.Fatalf("restore: %+v %v", restored, e)
	}
	var storageBytes int64
	for _, root := range []string{src.root, src.cfg.UploadDir, dst.root, dst.cfg.UploadDir} {
		if e = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			s, err := d.Info()
			if err != nil {
				return err
			}
			storageBytes += s.Size()
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("source + destination storage immediately after restore: %d bytes", storageBytes)
	b, e := os.ReadFile(filepath.Join(dst.cfg.UploadDir, "history.bin"))
	if e != nil || string(b) != "historical bytes 0" {
		t.Fatalf("file: %s %v", b, e)
	}
	c, e := dst.connect(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(ctx)
	var path string
	var valid bool
	if e = c.QueryRow(ctx, "SELECT file_path FROM files").Scan(&path); e != nil || path != filepath.Join(dst.cfg.UploadDir, "history.bin") {
		t.Fatalf("rebase: %s %v", path, e)
	}
	if e = c.QueryRow(ctx, "SELECT expires_at < now() AND revoked_at IS NOT NULL FROM sessions").Scan(&valid); e != nil || !valid {
		t.Fatalf("sessions: %v %v", valid, e)
	}
	if !dst.cfg.Control.EmailDisabled() {
		t.Fatal("email enabled")
	}
	// Capture rollback material, simulate an interrupted destructive replacement,
	// and recover twice without the uploaded password or a live originating job.
	recovery := filepath.Join(dst.root, "crash-recovery")
	if e = privateDir(recovery); e != nil {
		t.Fatal(e)
	}
	if _, e = dst.capture(ctx, recovery, "rollback"); e != nil {
		t.Fatal(e)
	}
	items, e := inventory(recovery)
	if e != nil {
		t.Fatal(e)
	}
	if e = atomicJSON(filepath.Join(recovery, "recovery.json"), items); e != nil {
		t.Fatal(e)
	}
	if e = dst.writeJournal(journal{Phase: "replacing", Recovery: "crash-recovery", EmailDisabled: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(dst.cfg.UploadDir, "history.bin")); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = dst.Recover(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if e = c.QueryRow(ctx, "SELECT body FROM evidence").Scan(&path); e != nil || path != "retained immutable snapshot" {
		t.Fatalf("rollback: %s %v", path, e)
	}
	// Verify native pre/data/post restoration against the complete real migration
	// schema too, including its immutable settlement/conversion/delivery triggers.
	if _, e = c.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); e != nil {
		t.Fatal(e)
	}
	db, e := database.Connect(ctx, dst.cfg.DSN)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.Migrate(ctx, os.DirFS("../database/migrations")); e != nil {
		t.Fatal(e)
	}
	full := filepath.Join(dst.root, "full-schema")
	if e = privateDir(full); e != nil {
		t.Fatal(e)
	}
	if _, e = dst.capture(ctx, full, "full schema"); e != nil {
		t.Fatal(e)
	}
	if e = dst.apply(ctx, full, full); e != nil {
		t.Fatal(e)
	}
}

func TestRejectExecutableData(t *testing.T) {
	for _, sql := range []string{"SELECT evil();\n", "COPY public.files (file_path) FROM PROGRAM 'evil';\n", "COPY public.files (file_path) FROM stdin;\nunterminated\n", "SELECT pg_catalog.setval('public.seq', 1, true); DROP SCHEMA public;\n"} {
		p := filepath.Join(t.TempDir(), "data.sql")
		if e := os.WriteFile(p, []byte(sql), 0600); e != nil {
			t.Fatal(e)
		}
		if e := importData(context.Background(), nil, p); e == nil {
			t.Fatalf("accepted %q", sql)
		}
	}
}
func TestReleaseAndRoots(t *testing.T) {
	m := storageManager(t)
	for _, v := range []string{"dev", "v1.2.3-dirty", "v1.2.3-4-gabcdef0", "1.02.3", "v1.2.3-01", ""} {
		cfg := m.cfg
		cfg.BuildKind = BuildRelease
		cfg.Version = v
		if _, e := New(cfg); e == nil {
			t.Fatalf("accepted unsupported release %q", v)
		}
		cfg.BuildKind = BuildDevelopment
		cfg.Commit = ""
		if _, e := New(cfg); e != nil {
			t.Fatalf("rejected development identity %q: %v", v, e)
		}
	}
	for _, v := range []string{"none", "dev", "", "123"} {
		cfg := m.cfg
		cfg.Commit = v
		if _, e := New(cfg); e == nil {
			t.Fatalf("accepted unidentified commit %q", v)
		}
	}
	cfg := m.cfg
	cfg.UploadDir = filepath.Join(cfg.StateDir, "nested")
	if _, e := New(cfg); e == nil {
		t.Fatal("accepted overlapping roots")
	}
	link := filepath.Join(t.TempDir(), "linked-uploads")
	if e := os.Symlink(m.cfg.UploadDir, link); e != nil {
		t.Fatal(e)
	}
	cfg = m.cfg
	cfg.UploadDir = link
	if _, e := New(cfg); e == nil {
		t.Fatal("accepted symlink root")
	}
	for _, p := range []string{"../a", "/a", "a/../b", "a\\b", "."} {
		if safeRelative(p) {
			t.Fatal(p)
		}
	}
}
