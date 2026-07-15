package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestCustomerCompanyMigration051BackfillsLegacySeedOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := customerChildMigrationDatabaseThrough049(t, dsn)
	if err := db.Migrate(ctx, customerChildMigrationFSThrough050(t)); err != nil {
		t.Fatalf("migrate through 050: %v", err)
	}

	var companyID, customerID, assetTypeID, userID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &companyID)
	if _, err := db.Pool.Exec(ctx, `UPDATE company_settings SET next_invoice_number=12 WHERE company_id=$1`, companyID); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM asset_types WHERE company_id=$1 ORDER BY id LIMIT 1`, companyID), &assetTypeID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'migration-051@example.test','x','Migration 051','admin') RETURNING id`, companyID), &userID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(display_name) VALUES('Companyless customer') RETURNING id`), &customerID)

	children := map[string]int64{}
	for table, query := range map[string]string{
		"projects":          `INSERT INTO projects(customer_id,name) VALUES($1,'Legacy project') RETURNING id`,
		"customer_contacts": `INSERT INTO customer_contacts(customer_id,first_name) VALUES($1,'Legacy') RETURNING id`,
		"locations":         `INSERT INTO locations(object_type,object_id,title) VALUES('customer',$1,'Legacy location') RETURNING id`,
	} {
		var id int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, query, customerID), &id)
		children[table] = id
	}
	var assetID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(customer_id,asset_type_id,name) VALUES($1,$2,'Legacy asset') RETURNING id`, customerID, assetTypeID), &assetID)
	children["assets"] = assetID

	var jobID, estimateID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(customer_id,project_id,job_type) VALUES($1,$2,'Legacy job') RETURNING id`, customerID, children["projects"]), &jobID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(customer_id,job_id,title) VALUES($1,$2,'Legacy estimate') RETURNING id`, customerID, jobID), &estimateID)
	direct := map[string]int64{
		"jobs":      jobID,
		"estimates": estimateID,
	}
	var invoiceID, itemID, timeEntryID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(customer_id,job_id,estimate_id,invoice_number,title) VALUES($1,$2,$3,51001,'Legacy invoice') RETURNING id`, customerID, jobID, estimateID), &invoiceID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO items(name) VALUES('Legacy migration 051 item') RETURNING id`), &itemID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO time_entries(user_id,clock_in) VALUES($1,now()) RETURNING id`, userID), &timeEntryID)
	direct["invoices"] = invoiceID
	direct["items"] = itemID
	direct["time_entries"] = timeEntryID

	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("migrate 051: %v", err)
	}
	var customerCompanyID *int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM customers WHERE id=$1`, customerID), &customerCompanyID)
	if customerCompanyID == nil || *customerCompanyID != companyID {
		t.Fatalf("customer company_id=%v, want %d", customerCompanyID, companyID)
	}
	for table, id := range children {
		var got *int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM `+table+` WHERE id=$1`, id), &got)
		if got == nil || *got != companyID {
			t.Errorf("%s company_id=%v, want %d", table, got, companyID)
		}
	}
	for table, id := range direct {
		var got *int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM `+table+` WHERE id=$1`, id), &got)
		if got == nil || *got != companyID {
			t.Errorf("%s company_id=%v, want %d", table, got, companyID)
		}
	}
	var nextInvoiceNumber int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT next_invoice_number FROM company_settings WHERE company_id=$1`, companyID), &nextInvoiceNumber)
	if nextInvoiceNumber != 51002 {
		t.Errorf("next_invoice_number=%d, want 51002", nextInvoiceNumber)
	}

	down, err := fs.ReadFile(MigrationFS(), "051_customer_company_backfill.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down 051: %v", err)
	}
	var afterDown *int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM customers WHERE id=$1`, customerID), &afterDown)
	if afterDown == nil || *afterDown != companyID {
		t.Fatalf("down changed customer ownership: %v", afterDown)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT next_invoice_number FROM company_settings WHERE company_id=$1`, companyID), &nextInvoiceNumber)
	if nextInvoiceNumber != 51002 {
		t.Fatalf("down changed next_invoice_number to %d", nextInvoiceNumber)
	}
}

func TestCustomerCompanyMigration051RejectsInvoiceNumberCollisionIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := customerChildMigrationDatabaseThrough049(t, dsn)
	if err := db.Migrate(ctx, customerChildMigrationFSThrough050(t)); err != nil {
		t.Fatalf("migrate through 050: %v", err)
	}

	var companyID, customerID, ownedInvoiceID, companylessInvoiceID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &companyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Invoice collision customer') RETURNING id`, companyID), &customerID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title) VALUES($1,$2,51003,'Owned invoice') RETURNING id`, companyID, customerID), &ownedInvoiceID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(customer_id,invoice_number,title) VALUES($1,51003,'Companyless invoice') RETURNING id`, customerID), &companylessInvoiceID)
	if _, err := db.Pool.Exec(ctx, `UPDATE company_settings SET next_invoice_number=7 WHERE company_id=$1`, companyID); err != nil {
		t.Fatal(err)
	}

	err := db.Migrate(ctx, MigrationFS())
	if err == nil || !strings.Contains(err.Error(), "invoice number collision for sole company") || !strings.Contains(err.Error(), "invoice_number 51003") {
		t.Fatalf("migration error=%v", err)
	}
	var ownedCompanyID, companylessCompanyID *int64
	var nextInvoiceNumber int64
	var applied bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM invoices WHERE id=$1`, ownedInvoiceID), &ownedCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM invoices WHERE id=$1`, companylessInvoiceID), &companylessCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT next_invoice_number FROM company_settings WHERE company_id=$1`, companyID), &nextInvoiceNumber)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='051_customer_company_backfill')`), &applied)
	if ownedCompanyID == nil || *ownedCompanyID != companyID || companylessCompanyID != nil || nextInvoiceNumber != 7 || applied {
		t.Fatalf("owned=%v companyless=%v next=%d applied=%v; want owned unchanged, companyless NULL, next 7, unapplied", ownedCompanyID, companylessCompanyID, nextInvoiceNumber, applied)
	}
}

func TestCustomerCompanyMigration051RejectsAmbiguousOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := customerChildMigrationDatabaseThrough049(t, dsn)
	if err := db.Migrate(ctx, customerChildMigrationFSThrough050(t)); err != nil {
		t.Fatalf("migrate through 050: %v", err)
	}
	var companyID, secondCompanyID, userID, customerID, projectID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &companyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Second company','second-company') RETURNING id`), &secondCompanyID)
	if secondCompanyID == companyID {
		t.Fatal("second company reused existing id")
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'ambiguous-051@example.test','x','Migration 051','admin') RETURNING id`, companyID), &userID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Owned customer') RETURNING id`, companyID), &customerID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO projects(company_id,customer_id,name) VALUES($1,$2,'Owned project') RETURNING id`, companyID, customerID), &projectID)

	var jobID, estimateID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(customer_id,project_id,job_type) VALUES($1,$2,'Ambiguous job') RETURNING id`, customerID, projectID), &jobID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(customer_id,job_id,title) VALUES($1,$2,'Ambiguous estimate') RETURNING id`, customerID, jobID), &estimateID)
	direct := map[string]int64{
		"jobs":      jobID,
		"estimates": estimateID,
	}
	var invoiceID, itemID, timeEntryID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(customer_id,job_id,estimate_id,invoice_number,title) VALUES($1,$2,$3,51002,'Ambiguous invoice') RETURNING id`, customerID, jobID, estimateID), &invoiceID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO items(name) VALUES('Ambiguous migration 051 item') RETURNING id`), &itemID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO time_entries(user_id,clock_in) VALUES($1,now()) RETURNING id`, userID), &timeEntryID)
	direct["invoices"] = invoiceID
	direct["items"] = itemID
	direct["time_entries"] = timeEntryID

	err := db.Migrate(ctx, MigrationFS())
	if err == nil || !strings.Contains(err.Error(), "ambiguous tenant ownership with 2 companies") {
		t.Fatalf("migration error=%v", err)
	}
	var applied bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='051_customer_company_backfill')`), &applied)
	if applied {
		t.Fatal("migration 051 was recorded after ambiguous ownership failure")
	}
	for table, id := range direct {
		var companyID *int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM `+table+` WHERE id=$1`, id), &companyID)
		if companyID != nil {
			t.Errorf("%s company_id=%v, want NULL after rollback", table, *companyID)
		}
	}
}
