package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPreflightRequiresExplicitDatabaseAndDoesNotDiscloseConnectionSecrets(t *testing.T) {
	for _, dsn := range []string{"", "postgres://user:secret-probe@[invalid"} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), dsn, &out, &diagnostics); code != 1 {
			t.Fatalf("exit=%d, want failure", code)
		}
		if out.Len() != 0 || diagnostics.Len() == 0 || strings.Contains(diagnostics.String(), "secret-probe") {
			t.Fatalf("unsafe diagnostics or false inventory: out=%q diagnostics=%q", out.String(), diagnostics.String())
		}
	}
}

func TestPreflightCommandInventoriesMigratedDatabaseReadOnly(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := fmt.Sprintf("authorization_command_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &database.DB{Pool: pool}
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() > "054_backup_activity.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacy[e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := db.Migrate(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	// Enforce read-only at connection level as well as in the inventory's
	// transaction. Accidentally invoking migrations or writing audit now fails.
	readOnlyDSN := dsn + " search_path=" + schema + " default_transaction_read_only=on"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		q.Set("default_transaction_read_only", "on")
		u.RawQuery = q.Encode()
		readOnlyDSN = u.String()
	}
	var out, diagnostics bytes.Buffer
	if code := run(ctx, readOnlyDSN, &out, &diagnostics); code != 0 {
		t.Fatalf("empty migrated database exit=%d diagnostics=%s", code, diagnostics.String())
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,name,password_hash,role) VALUES(123,'review@example.test','Recorded user','secret-probe','dispatcher')`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	if code := run(ctx, readOnlyDSN, &out, &diagnostics); code != 2 {
		t.Fatalf("existing-installation exit=%d diagnostics=%s", code, diagnostics.String())
	}
	var report database.AuthorizationInventory
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Users) != 1 || report.Users[0].ID != 123 || !report.ReviewRequired || strings.Contains(out.String(), "secret-probe") {
		t.Fatalf("invalid or secret-bearing inventory: %+v", report)
	}
}
