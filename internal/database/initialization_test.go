package database

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestInterruptedFreshInitializationResumes(t *testing.T) {
	for _, cutoff := range []string{"000", "035z", "043z"} {
		t.Run(cutoff, func(t *testing.T) { interruptedInitialization(t, cutoff, false) })
	}
}

func TestInterruptedInitializationDoesNotAuthorizeChangedData(t *testing.T) {
	interruptedInitialization(t, "035z", true)
}

func interruptedInitialization(t *testing.T, cutoff string, changed bool) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", "")
	ctx := context.Background()
	root, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := fmt.Sprintf("initialization_%d", time.Now().UnixNano())
	if _, err = root.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	isolated := statusMigrationDSN(t, dsn, schema)
	db, err := Connect(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		interrupted[entry.Name()] = &fstest.MapFile{Data: data}
	}
	// Fail after committed seed migrations, before workflow conversion/bootstrap.
	interrupted[cutoff+"_interruption.up.sql"] = &fstest.MapFile{Data: []byte(`SELECT 1/0`)}
	if err = db.Migrate(ctx, interrupted); err == nil || !strings.Contains(err.Error(), cutoff+"_interruption") {
		t.Fatalf("expected interrupted initialization, got %v", err)
	}
	db.Close()
	// A different connection/process has no in-memory knowledge of first start.
	db, err = Connect(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if changed {
		if _, err := db.Pool.Exec(ctx, `UPDATE statuses SET name='Operator changed meaning' WHERE id=(SELECT min(id) FROM statuses)`); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err == nil || !strings.Contains(err.Error(), "workflow review required") {
			t.Fatalf("changed initialization must require review: %v", err)
		}
		var retained bool
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM statuses WHERE name='Operator changed meaning')`), &retained)
		if !retained {
			t.Fatal("changed evidence lost")
		}
		return
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("fresh installation cannot resume: %v", err)
	}
	var users, categories int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM users`), &users)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(DISTINCT category_key) FROM statuses`), &categories)
	if users != 0 || categories != 23 {
		t.Fatalf("bootstrap/seed contract: users=%d categories=%d", users, categories)
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("completed restart: %v", err)
	}
}
