package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
)

// Uses only unique databases and NO-CREATEDB roles in an explicitly supplied
// disposable cluster. Exercise Create through its public operation status.
func TestBackupCreateUnsupportedLayout(t *testing.T) {
	for _, tc := range []struct {
		name, setup, category, message, reason string
	}{
		{"extra-schemas", "CREATE SCHEMA private_schema_marker", "database-extra-schemas", "Backup requires a public-only application database, but additional schemas are present. Ask the deployment operator to inspect them before any cleanup, then retry.", "additional non-system schemas are present"},
		{"extensions", "CREATE EXTENSION backup_preflight_fixture", "database-extensions", "Backup does not support database extensions other than plpgsql. Additional extensions are present. Ask the deployment operator to inspect their dependencies before any cleanup, then retry.", "extensions other than plpgsql are present"},
		{"large-objects", "SELECT lo_from_bytea(0, convert_to('private_large_object_marker', 'UTF8'))", "database-large-objects", "Backup does not support PostgreSQL large objects, but this database contains them. Ask the deployment operator to inspect their use before any cleanup, then retry.", "PostgreSQL large objects are present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, db := preflightFixture(t)
			ctx := context.Background()
			if _, err := db.Pool.Exec(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx, "CREATE TABLE public.private_probe(value text); INSERT INTO public.private_probe VALUES ('private_customer_marker')"); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(m.cfg.UploadDir, "private_file_marker")
			if err := os.WriteFile(file, []byte("private_file_contents"), 0600); err != nil {
				t.Fatal(err)
			}
			// Compare complete logical dumps, including fixture data, before/after
			// rejection. Never print SQL or payloads on failure.
			snapshot := func() string {
				t.Helper()
				f, err := os.CreateTemp(t.TempDir(), "source-*.sql")
				if err != nil {
					t.Fatal(err)
				}
				err = m.command(ctx, "pg_dump", f, "--format=plain", "--no-owner", "--no-acl")
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("source snapshot failed")
				}
				hash, err := schemaHash(f.Name())
				if err != nil {
					t.Fatal(err)
				}
				return hash
			}
			before := snapshot()
			var reported Diagnostic
			m.cfg.ReportFailure = func(d Diagnostic) { reported = d }
			op, err := m.StartBackup(1, "private_source_marker", "private_password_marker")
			if err != nil {
				t.Fatal(err)
			}
			m.wg.Wait()
			op, err = m.Status(1, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if op.Phase != "failed" || op.Failure.Phase != "preflight" || string(op.Failure.Category) != tc.category || op.Failure.Message != tc.message || op.Error != tc.message {
				t.Errorf("Create failure: phase=%s category=%s message=%q; want category=%s message=%q", op.Failure.Phase, op.Failure.Category, op.Failure.Message, tc.category, tc.message)
			}
			if r, _, _, err := m.OpenDownload(1, op.ID); err == nil {
				_ = r.Close()
				t.Error("rejected Create exposed an archive")
			}
			if err := filepath.WalkDir(m.root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && (strings.HasSuffix(path, ".age") || strings.HasSuffix(path, ".dump")) {
					t.Error("rejected Create retained archive material")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if snapshot() != before {
				t.Error("Create rejection mutated source database")
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != "private_file_contents" {
				t.Error("Create rejection mutated uploads")
			}
			stored, err := os.ReadFile(filepath.Join(m.root, "last-failure.json"))
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic Diagnostic
			if err := json.Unmarshal(stored, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if diagnostic.Failure != op.Failure || reported.Failure != op.Failure || op.Failure.DiagnosticID == "" {
				t.Error("uncorrelated failure diagnostic")
			}
			if len(diagnostic.Causes) != 1 || string(diagnostic.Causes[0].Category) != tc.category || diagnostic.Causes[0].Reason != tc.reason {
				t.Error("missing distinct typed layout cause")
			}
			emitted, err := json.Marshal(reported)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{op.ID, m.cfg.DSN, "private_"} {
				if strings.Contains(string(stored), secret) || strings.Contains(string(emitted), secret) {
					t.Error("diagnostic leaked private data or a capability")
				}
			}
		})
	}
}

func preflightFixture(t *testing.T) (*Manager, *database.DB) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL admin URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	name := fmt.Sprintf("backup_preflight_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOCREATEDB NOSUPERUSER"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE "+ident) })
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User, u.Path = url.User(name), "/"+name
	root := t.TempDir()
	control, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	m, err := New(Config{DSN: u.String(), UploadDir: filepath.Join(root, "uploads"), StateDir: control.StateDir(), BuildKind: BuildDevelopment, Version: "dev", Commit: "none", Control: control, ResetConnections: db.Pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.cfg.UploadDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	return m, db
}
