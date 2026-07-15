package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSeedAssignsResolvedCompanyToAllCreatedEntitiesIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_seed_%d", time.Now().UnixNano())
	if _, err = admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	schemaDSN := migrationSearchPath(t, dsn, schema)
	db, err := Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var companyID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM company_settings`), &companyID)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO users(id,company_id,email,password_hash,name,role) VALUES(51001,$1,'seed-admin@example.test','x','Seed Admin','admin')`, companyID); err != nil {
		t.Fatal(err)
	}

	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB)))
	t.Cleanup(func() { _ = client.Close() })
	if err = Seed(ctx, client); err != nil {
		t.Fatalf("seed: %v", err)
	}

	expected := map[string]int{
		"customers":    5,
		"assets":       8,
		"items":        5,
		"projects":     5,
		"jobs":         5,
		"estimates":    5,
		"invoices":     5,
		"time_entries": 5,
	}
	for table, want := range expected {
		var total, owned int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE company_id=$1) FROM `+table, companyID), &total, &owned)
		if total != want || owned != want {
			t.Errorf("%s total=%d owned=%d, want %d", table, total, owned, want)
		}
	}
	var seededTimeEntries int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM time_entries WHERE user_id=51001 AND company_id=$1`, companyID), &seededTimeEntries)
	if seededTimeEntries != 5 {
		t.Errorf("tenant admin time entries=%d, want 5", seededTimeEntries)
	}
	for _, tc := range []struct {
		table              string
		objectType         string
		wantDistinctStates int
	}{
		{"jobs", "job", 4},
		{"estimates", "estimate", 3},
		{"invoices", "invoice", 2},
	} {
		var resolved, distinctStates int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT s.category_key)
			FROM `+tc.table+` record
			JOIN statuses s ON s.id=record.status_id AND s.company_id=record.company_id
			JOIN status_workflows w ON w.id=s.workflow_id AND w.company_id=record.company_id
			WHERE record.company_id=$1 AND w.object_type=$2`, companyID, tc.objectType), &resolved, &distinctStates)
		if resolved != 5 || distinctStates != tc.wantDistinctStates {
			t.Errorf("%s resolved statuses=%d distinct categories=%d, want 5 and %d", tc.table, resolved, distinctStates, tc.wantDistinctStates)
		}
	}
}
