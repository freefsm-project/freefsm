package database

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSettlementMigration042Integration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL migration tests")
	}

	t.Run("materializes zero-company legacy tenancy", func(t *testing.T) {
		db, ctx := migrationDatabaseThrough041(t, dsn)
		if _, err := db.Pool.Exec(ctx, `UPDATE company_settings SET business_name='Legacy Service', timezone='America/New_York'`); err != nil {
			t.Fatal(err)
		}
		var userID, customerID int64
		if err := db.Pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,name,role) VALUES($1,'x','Legacy Actor','admin') RETURNING id`, fmt.Sprintf("legacy-%d@example.test", time.Now().UnixNano())).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `INSERT INTO customers(display_name) VALUES('Legacy Customer') RETURNING id`).Scan(&customerID); err != nil {
			t.Fatal(err)
		}
		var statusID int64
		if err := db.Pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.object_type='invoice' AND lower(s.name)='sent' LIMIT 1`).Scan(&statusID); err != nil {
			t.Fatal(err)
		}
		var invoiceID int64
		if err := db.Pool.QueryRow(ctx, `INSERT INTO invoices(customer_id,status_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate,payments) VALUES($1,$2,1,'Legacy',CURRENT_DATE,CURRENT_DATE,'[]','0','[]') RETURNING id`, customerID, statusID).Scan(&invoiceID); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatalf("migrate 042: %v", err)
		}
		var companyID, actorCompanyID int64
		var name, slug, timezone string
		if err := db.Pool.QueryRow(ctx, `SELECT c.id,c.name,c.slug,cs.timezone,u.company_id FROM companies c JOIN company_settings cs ON cs.company_id=c.id JOIN users u ON u.id=$1`, userID).Scan(&companyID, &name, &slug, &timezone, &actorCompanyID); err != nil {
			t.Fatal(err)
		}
		if name != "Legacy Service" || slug != "legacy" || timezone != "America/New_York" || actorCompanyID != companyID {
			t.Fatalf("company=%d name=%q slug=%q timezone=%q actorCompanyID=%d", companyID, name, slug, timezone, actorCompanyID)
		}
		for _, table := range []string{"users", "company_settings", "customers", "invoices", "statuses", "status_workflows", "asset_types"} {
			var missing int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE company_id IS NULL OR company_id<>$1`, companyID).Scan(&missing); err != nil {
				t.Fatal(err)
			}
			if missing != 0 {
				t.Errorf("%s has %d records outside legacy company", table, missing)
			}
		}
		var invoiceCompanyID, customerCompanyID int64
		if err := db.Pool.QueryRow(ctx, `SELECT i.company_id,c.company_id FROM invoices i JOIN customers c ON c.id=i.customer_id WHERE i.id=$1`, invoiceID).Scan(&invoiceCompanyID, &customerCompanyID); err != nil {
			t.Fatal(err)
		}
		if invoiceCompanyID != companyID || customerCompanyID != companyID {
			t.Fatalf("invoice company=%d customer company=%d want %d", invoiceCompanyID, customerCompanyID, companyID)
		}
	})

	t.Run("backfills one company and preserves ownership", func(t *testing.T) {
		db, ctx := migrationDatabaseThrough041(t, dsn)
		var companyID int64
		if err := db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Existing','existing') RETURNING id`).Scan(&companyID); err != nil {
			t.Fatal(err)
		}
		var ownedUserID int64
		if err := db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,$2,'x','Owned','admin') RETURNING id`, companyID, fmt.Sprintf("owned-%d@example.test", time.Now().UnixNano())).Scan(&ownedUserID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO customers(display_name) VALUES('Unowned')`); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatalf("migrate 042: %v", err)
		}
		var owner int64
		if err := db.Pool.QueryRow(ctx, `SELECT company_id FROM users WHERE id=$1`, ownedUserID).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		if owner != companyID {
			t.Fatalf("existing owner changed to %d, want %d", owner, companyID)
		}
		var missing int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customers WHERE company_id IS NULL`).Scan(&missing); err != nil || missing != 0 {
			t.Fatalf("unowned customers=%d err=%v", missing, err)
		}
	})

	t.Run("rejects ambiguous tenancy and rolls back", func(t *testing.T) {
		db, ctx := migrationDatabaseThrough041(t, dsn)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO companies(name,slug) VALUES('One','one'),('Two','two')`); err != nil {
			t.Fatal(err)
		}
		err := db.Migrate(ctx, MigrationFS())
		if err == nil || !strings.Contains(err.Error(), "ambiguous tenant ownership with 2 companies") || !strings.Contains(err.Error(), "company_settings=1") {
			t.Fatalf("migration error = %v", err)
		}
		var missing int
		var applied, staging bool
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM company_settings WHERE company_id IS NULL`).Scan(&missing); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='042_normalized_invoice_settlement'), to_regclass('settlement_migration_invoice_totals') IS NOT NULL`).Scan(&applied, &staging); err != nil {
			t.Fatal(err)
		}
		if missing != 1 || applied || staging {
			t.Fatalf("missing=%d applied=%v staging=%v", missing, applied, staging)
		}
	})

	t.Run("converts valid legacy settlement history", func(t *testing.T) {
		db, ctx := migrationDatabaseThrough041(t, dsn)
		invoiceID := seedLegacySettlement(t, ctx, db, `[{"id":"p1","amount":70,"method":"cash","date":"2026-01-01"},{"id":"p2","amount":50,"method":"check","date":"2026-01-02"}]`, `[{"title":"Work","quantity":1,"unit_price":100}]`, "Paid")
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatalf("migrate 042: %v", err)
		}
		var allocated, credit int64
		var state, status string
		if err := db.Pool.QueryRow(ctx, `SELECT coalesce(sum(a.amount_cents),0),coalesce((SELECT sum(original_amount_cents) FROM customer_credits),0),i.settlement_state,s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id LEFT JOIN payment_invoice_allocations a ON a.invoice_id=i.id WHERE i.id=$1 GROUP BY i.settlement_state,s.category_key`, invoiceID).Scan(&allocated, &credit, &state, &status); err != nil {
			t.Fatal(err)
		}
		if allocated != 10000 || credit != 2000 || state != "paid" || status != "invoice:sent" {
			t.Fatalf("allocated=%d credit=%d state=%s status=%s", allocated, credit, state, status)
		}
		var paymentsColumn, staging, removedStatuses bool
		_ = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='invoices' AND column_name='payments')`).Scan(&paymentsColumn)
		_ = db.Pool.QueryRow(ctx, `SELECT to_regclass('settlement_migration_payments') IS NOT NULL`).Scan(&staging)
		_ = db.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.object_type='invoice' AND s.category_key IN ('invoice:paid','invoice:partially_paid'))`).Scan(&removedStatuses)
		if paymentsColumn || staging || removedStatuses {
			t.Fatalf("paymentsColumn=%v staging=%v removedStatuses=%v", paymentsColumn, staging, removedStatuses)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE invoice_payments SET notes='changed'`); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("immutable trigger error = %v", err)
		}
	})

	failures := []struct {
		name, payments, items, status string
	}{
		{"null payments", `null`, `[]`, "Sent"},
		{"malformed payments", `[{"amount":"bad","method":"cash","date":"2026-01-01"}]`, `[]`, "Sent"},
		{"invalid method", `[{"amount":1,"method":"wire","date":"2026-01-01"}]`, `[]`, "Sent"},
		{"invalid date", `[{"amount":1,"method":"cash","date":"not-a-date"}]`, `[]`, "Sent"},
		{"future date", `[{"amount":1,"method":"cash","date":"2999-01-01"}]`, `[]`, "Sent"},
		{"negative total", `[]`, `[{"title":"Credit","quantity":1,"unit_price":-1}]`, "Sent"},
		{"void with payments", `[{"amount":1,"method":"cash","date":"2026-01-01"}]`, `[]`, "Void"},
	}
	for _, tc := range failures {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			db, ctx := migrationDatabaseThrough041(t, dsn)
			seedLegacySettlement(t, ctx, db, tc.payments, tc.items, tc.status)
			if err := db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("migration unexpectedly succeeded")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(context.Context, *DB, int64) error
	}{
		{"orphan customer", func(ctx context.Context, db *DB, invoiceID int64) error {
			_, err := db.Pool.Exec(ctx, `UPDATE invoices SET customer_id=NULL WHERE id=$1`, invoiceID)
			return err
		}},
		{"cross-company customer", func(ctx context.Context, db *DB, invoiceID int64) error {
			var companyID, customerID int64
			if err := db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other',$1) RETURNING id`, fmt.Sprintf("other-%d", invoiceID)).Scan(&companyID); err != nil {
				return err
			}
			if err := db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Other') RETURNING id`, companyID).Scan(&customerID); err != nil {
				return err
			}
			_, err := db.Pool.Exec(ctx, `UPDATE invoices SET customer_id=$1 WHERE id=$2`, customerID, invoiceID)
			return err
		}},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			db, ctx := migrationDatabaseThrough041(t, dsn)
			invoiceID := seedLegacySettlement(t, ctx, db, `[]`, `[]`, "Sent")
			if err := tc.mutate(ctx, db, invoiceID); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("migration unexpectedly succeeded")
			}
		})
	}
}

func TestConversionMigration043BackfillsLegacyRelationOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL migration tests")
	}
	db, ctx := migrationDatabaseThrough041(t, dsn)
	seedLegacySettlement(t, ctx, db, "[]", "[]", "Sent")
	through043 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "044_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through043[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, through043); err != nil {
		t.Fatal(err)
	}
	down, err := fs.ReadFile(MigrationFS(), "043_estimate_invoice_conversion.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down 043: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, `DELETE FROM schema_migrations WHERE name='043_estimate_invoice_conversion'`); err != nil {
		t.Fatal(err)
	}
	var companyID, actorID, estimateID int64
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE company_id=$1 LIMIT 1`, companyID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	var customerID int64
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM customers WHERE company_id=$1 LIMIT 1`, companyID).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Legacy relations') RETURNING id`, companyID, customerID).Scan(&estimateID); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES(NULL,'estimate',$1,'a','b','text/plain',1,'p',$2)`,
		`INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES(NULL,'estimate',$1,$2,'legacy')`,
		`INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id) VALUES(NULL,$2,'legacy','estimate',$1)`,
	}
	for _, statement := range statements {
		if _, err = db.Pool.Exec(ctx, statement, estimateID, actorID); err != nil {
			t.Fatal(err)
		}
	}
	var tagID int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'Legacy') RETURNING id`, companyID).Scan(&tagID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES(NULL,$1,'estimate',$2)`, tagID, estimateID); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("migrate 043: %v", err)
	}
	for _, table := range []string{"files", "comments", "activity_logs", "tag_links"} {
		var bad int
		if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE company_id IS DISTINCT FROM $1`, companyID).Scan(&bad); err != nil || bad != 0 {
			t.Fatalf("%s bad ownership=%d err=%v", table, bad, err)
		}
	}
}

func migrationDatabaseThrough041(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_migration_%d", time.Now().UnixNano())
	if _, err = admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	db, err := Connect(ctx, migrationSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	all := MigrationFS()
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	through041 := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "042_" {
			continue
		}
		data, readErr := fs.ReadFile(all, entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through041[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through041); err != nil {
		t.Fatalf("migrate through 041: %v", err)
	}
	var hasLegacyPayments, hasSettlementState bool
	if err = db.Pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='invoices' AND column_name='payments'),
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='invoices' AND column_name='settlement_state')`).Scan(&hasLegacyPayments, &hasSettlementState); err != nil {
		t.Fatalf("inspect schema through 041: %v", err)
	}
	if !hasLegacyPayments || hasSettlementState {
		t.Fatalf("schema through 041 has legacy payments=%v settlement_state=%v", hasLegacyPayments, hasSettlementState)
	}
	return db, ctx
}

func seedLegacySettlement(t *testing.T, ctx context.Context, db *DB, payments, items, statusName string) int64 {
	t.Helper()
	var companyID, customerID, invoiceID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Migration',$1) RETURNING id`, fmt.Sprintf("migration-%d", time.Now().UnixNano())).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,business_name,timezone) VALUES($1,'Migration','UTC')`, companyID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, companyID).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,$2,'x','Actor','admin')`, companyID, fmt.Sprintf("migration-%d@example.test", companyID)); err != nil {
		t.Fatal(err)
	}
	var statusID int64
	if err := db.Pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.object_type='invoice' AND lower(s.name)=lower($1) ORDER BY s.id LIMIT 1`, statusName).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate,payments) VALUES($1,$2,$3,1,'Legacy',CURRENT_DATE,CURRENT_DATE,$4::jsonb,'0',$5::jsonb) RETURNING id`, companyID, customerID, statusID, items, payments).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	return invoiceID
}

func migrationSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
