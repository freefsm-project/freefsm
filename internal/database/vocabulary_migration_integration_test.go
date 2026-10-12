package database

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVocabularyUpgradePreflight(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, table := range []string{"tags", "asset_types", "asset_statuses"} {
		t.Run(table, func(t *testing.T) {
			db, ctx := tagMigrationDatabaseThrough045(t, dsn)
			if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) SELECT id,'Collision' FROM companies; INSERT INTO `+table+`(company_id,name) SELECT id,E' \tCOLLISION\n' FROM companies`); err != nil {
				t.Fatal(err)
			}
			var before, after int
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &before)
			findings, err := db.VocabularyPreflight(ctx)
			if err != nil || len(findings) != 1 || !strings.Contains(findings[0], table) {
				t.Fatalf("findings=%v err=%v", findings, err)
			}
			if err := db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("normalization collision silently accepted")
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &after)
			if before != after {
				t.Fatal("preflight applied earlier migrations")
			}
			var count int
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE lower(btrim(name,E' \t\n'))='collision'`), &count)
			if count != 2 {
				t.Fatalf("distinct definitions merged: %d", count)
			}
		})
	}
}

func TestVocabularyUnicodeLegacyNamesReserveDatabaseIdentity(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, table := range []string{"tags", "asset_types", "asset_statuses"} {
		t.Run(table, func(t *testing.T) {
			db, ctx := tagMigrationDatabaseThrough045(t, dsn)
			var company int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
			spaces := []string{"\u0085", "\u00a0", "\u1680", "\u2000", "\u2001", "\u2002", "\u2003", "\u2004", "\u2005", "\u2006", "\u2007", "\u2008", "\u2009", "\u200a", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000"}
			for i, space := range spaces {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, space+fmt.Sprintf("Priority%d", i)+space); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, "\u00a0Équipement\u2003"); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(ctx, MigrationFS()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx, `UPDATE `+table+` SET retired=true`); err != nil {
				t.Fatal(err)
			}
			for i := range spaces {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, fmt.Sprintf("priority%d", i)); err == nil {
					t.Fatalf("retired legacy %U identity was not reserved", []rune(spaces[i])[0])
				}
			}
			if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, "éQUIPEMENT"); err == nil {
				t.Fatal("database case normalization disagrees with application")
			}
			for _, space := range spaces {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, space); err == nil {
					t.Fatalf("Unicode whitespace-only name accepted: %U", []rune(space)[0])
				}
			}
			// These UTF-8 characters share bytes with Unicode whitespace. They must not
			// be damaged by byte-set trimming, nor conflated with plain 'safe'.
			for _, name := range []string{"©safe©", "àsafeà", "\u200bsafe\u200b", "safe"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, name); err != nil {
					t.Fatalf("distinct spelling %q rejected: %v", name, err)
				}
			}
			var stored string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT name FROM `+table+` WHERE name=$1`, "\u00a0Équipement\u2003"), &stored)
			if stored != "\u00a0Équipement\u2003" {
				t.Fatal("legacy spelling rewritten")
			}
		})
	}
}

func TestVocabularyUpgradePreservesFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, tag, typ, status, asset int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name,deleted_at) VALUES($1,'Archived legacy',now()) RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name,color) VALUES($1,' Legacy tag ','#123456') RETURNING id`, company), &tag)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM asset_types ORDER BY id LIMIT 1`), &typ)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM asset_statuses ORDER BY id LIMIT 1`), &status)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,asset_status_id,name,deleted_at) VALUES($1,$2,$3,$4,'Legacy asset',now()) RETURNING id`, company, customer, typ, status), &asset)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'asset',$3)`, company, tag, asset); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
	}
	var name string
	var actualType, actualStatus, actualTag int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT t.name,a.asset_type_id,a.asset_status_id,l.tag_id FROM assets a JOIN tag_links l ON l.object_type='asset' AND l.object_id=a.id JOIN tags t ON t.id=l.tag_id WHERE a.id=$1 AND a.deleted_at IS NOT NULL`, asset), &name, &actualType, &actualStatus, &actualTag)
	if name != " Legacy tag " || actualType != typ || actualStatus != status || actualTag != tag {
		t.Fatalf("upgrade rewrote recorded facts: %q %d %d %d", name, actualType, actualStatus, actualTag)
	}
	var count int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type IN ('tag','asset_type','asset_status')`), &count)
	if count != 0 {
		t.Fatal("invented historical events")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM asset_statuses WHERE id=$1`, status); err == nil {
		t.Fatal("archived status reference erased")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tags WHERE id=$1`, tag); err == nil {
		t.Fatal("archived tag reference erased")
	}
}

func TestVocabularyUnicodePreflight(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO asset_types(name) VALUES($1),($2)`, "Équipement", "\u00a0éQUIPEMENT\u00a0"); err != nil {
		t.Fatal(err)
	}
	findings, err := db.VocabularyPreflight(ctx)
	if err != nil || len(findings) != 1 {
		t.Fatalf("Unicode collisions %v %v", findings, err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("Unicode collision upgraded without review")
	}
}

func TestVocabularyUpgradeFromBeforeCompanyColumns(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn, "021_")
	var customer, tag, link int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(display_name) VALUES('Original customer') RETURNING id`), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name,color) VALUES('Original tag','#123456') RETURNING id`), &tag)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tag_links(tag_id,object_type,object_id) VALUES($1,'customer',$2) RETURNING id`, tag, customer), &link)
	if findings, err := db.VocabularyPreflight(ctx); err != nil || len(findings) != 0 {
		t.Fatalf("valid pre-021 inventory: %v %v", findings, err)
	}
	// This fixture also predates archive/conversion columns. Export the actual
	// legacy inventory read-only and explicitly review its seeded workflows.
	var before, after int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &before)
	inventory, err := db.WorkflowPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &after)
	if before != after {
		t.Fatal("workflow inventory applied migrations")
	}
	// Migration 032 seeds an additional Invoiced status. Review at the 044
	// boundary so the mapping covers actual IDs, including that new seed.
	if err := db.Migrate(ctx, settlementMigrationFS(t)); err != nil {
		t.Fatal(err)
	}
	inventory, err = db.WorkflowPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Statuses []struct {
			ID         int64 `json:"id"`
			WorkflowID int64 `json:"workflow_id"`
		} `json:"statuses"`
		Workflows []struct {
			ID   int64  `json:"id"`
			Type string `json:"object_type"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal(inventory.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	workflowTypes := map[int64]string{}
	for _, workflow := range evidence.Workflows {
		workflowTypes[workflow.ID] = workflow.Type
	}
	for _, status := range evidence.Statuses {
		// Explicit fixture review, independent of legacy display labels.
		category := map[string]string{"job": "job:pending", "project": "project:pending", "estimate": "estimate:estimate", "invoice": "invoice:invoiced"}[workflowTypes[status.WorkflowID]]
		if category == "" {
			t.Fatalf("unreviewed fixture workflow %d", status.WorkflowID)
		}
		inventory.Mappings[fmt.Sprint(status.ID)] = category
	}
	review, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workflow-review.json")
	if err := os.WriteFile(path, review, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", path)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var name, color string
	var actualTag, actualCustomer int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT t.name,t.color,l.tag_id,l.object_id FROM tag_links l JOIN tags t ON t.id=l.tag_id WHERE l.id=$1 AND l.object_type='customer'`, link), &name, &color, &actualTag, &actualCustomer)
	if name != "Original tag" || color != "#123456" || actualTag != tag || actualCustomer != customer {
		t.Fatalf("changed pre-021 facts: %q %q %d %d", name, color, actualTag, actualCustomer)
	}
}

func TestVocabularyPreservedUnicodeNamesStayReserved(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, table := range []string{"tags", "asset_types", "asset_statuses"} {
		t.Run(table, func(t *testing.T) {
			db, ctx := tagMigrationDatabaseThrough045(t, dsn)
			var company, id int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
			legacy := "\u00a0Priority\u2003"
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2) RETURNING id`, company, legacy), &id)
			if err := db.Migrate(ctx, MigrationFS()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx, `UPDATE `+table+` SET retired=true WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"priority", "\u0085PRIORITY\u3000", "\u202fPriority\u205f"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, name); err == nil {
					t.Fatalf("retired legacy name admitted equivalent %q", name)
				}
			}
			for _, name := range []string{"\u00a0\u2003\u3000", "\u0085\u202f\u205f"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, name); err == nil {
					t.Fatalf("Unicode blank name admitted %q", name)
				}
			}
			// © shares UTF-8 bytes with NBSP. Trimming a byte set would corrupt it.
			if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, "\u00a0© Label ©\u2003"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx, `INSERT INTO `+table+`(company_id,name) VALUES($1,$2)`, company, "© label ©"); err == nil {
				t.Fatal("whole UTF-8 sequences were not preserved")
			}
			var saved string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT name FROM `+table+` WHERE id=$1`, id), &saved)
			if saved != legacy {
				t.Fatalf("legacy label rewritten: %q", saved)
			}
		})
	}
}
