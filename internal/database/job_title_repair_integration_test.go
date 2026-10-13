package database

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"
)

// Retains the independent merge-33 whitespace probes and adds the complete Go
// White_Space set plus non-space Unicode controls. Expected blankness comes
// from the application's Go predicate, not PostgreSQL's locale or SQL helper.
func TestJobRepairWhitespaceUpgrade(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	var allSpace strings.Builder
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.IsSpace(r) {
			allSpace.WriteRune(r)
		}
	}
	for _, tc := range []struct{ name, title string }{
		{"empty", ""}, {"spaces", "   "}, {"tabs", "\t\t"}, {"newlines", "\r\n"}, {"mixed_ascii", " \t\r\n "},
		{"nbsp", "\u00a0"}, {"em_space", "\u2003"}, {"mixed_unicode", "\u0085\u00a0\u2003\u2028\u2029\u3000"},
		{"all_go_white_space", allSpace.String()}, {"nonblank_surrounded", "\t Recorded work \u2003"}, {"zero_width_is_not_blank", "\u200b"}, {"bom_is_not_blank", "\ufeff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "job-title-review")
			old := fstest.MapFS{}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() || entry.Name() >= "074_" {
					continue
				}
				data, err := fs.ReadFile(MigrationFS(), entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				old[entry.Name()] = &fstest.MapFile{Data: data}
			}
			if err := db.Migrate(ctx, old); err != nil {
				t.Fatal(err)
			}
			var contact, id int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Recorded Contact') RETURNING id`, a.company, a.customer), &contact)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,customer_contact_id) VALUES($1,$2,$3,$4) RETURNING id`, a.company, a.customer, tc.title, contact), &id)
			const snapshot = `SELECT jsonb_build_object('job',to_jsonb(j),'contact',(SELECT to_jsonb(c) FROM customer_contacts c WHERE c.id=j.customer_contact_id),'customer',(SELECT to_jsonb(c) FROM customers c WHERE c.id=j.customer_id))::text FROM jobs j WHERE id=$1`
			var before, after string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot, id), &before)
			migrationErr := db.Migrate(ctx, MigrationFS())
			mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot, id), &after)
			var contacts, blockers, applied bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_regclass('job_contacts') IS NOT NULL,to_regclass('job_customer_blockers') IS NOT NULL,EXISTS(SELECT 1 FROM schema_migrations WHERE name='074_job_planning')`), &contacts, &blockers, &applied)
			if before != after {
				t.Fatal("cutover rewrote recorded Job/Customer/Contact facts")
			}
			if strings.TrimSpace(tc.title) == "" {
				if migrationErr == nil || !strings.Contains(migrationErr.Error(), "Administrator review") {
					t.Errorf("blank title %q bypassed review: %v", tc.title, migrationErr)
				}
				if contacts || blockers || applied {
					t.Errorf("blank-title migration published schema/marker: %v %v %v", contacts, blockers, applied)
				}
			} else {
				if migrationErr != nil {
					t.Fatal(migrationErr)
				}
				if !contacts || !blockers || !applied {
					t.Fatal("nonblank control did not migrate")
				}
				var linked bool
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_contacts WHERE job_id=$1 AND contact_id=$2)`, id, contact), &linked)
				if !linked {
					t.Fatal("recorded link lost")
				}
				if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET job_type=$2 WHERE id=$1`, id, allSpace.String()); err == nil {
					t.Fatal("database invariant accepted Go-blank title")
				}
				mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot, id), &after)
				if before != after {
					t.Fatal("rejected title update changed facts")
				}
			}
		})
	}
}

func TestJobRepairPreviouslyAppliedPlanningUpgrade(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, title := range []string{"\u00a0\u2003", "\t Recorded title \u2003"} {
		t.Run(fmt.Sprintf("%q", title), func(t *testing.T) {
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "job-forward-review")
			old := fstest.MapFS{}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() || entry.Name() >= "075_" {
					continue
				}
				data, err := fs.ReadFile(MigrationFS(), entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if entry.Name() == "074_job_planning.up.sql" {
					// Reproduce the original committed 074 predicate and absent helper;
					// subsequent migrations must repair already-applied installations.
					sql := string(data)
					start := strings.Index(sql, "DO $$ BEGIN")
					if start < 0 {
						t.Fatal("missing preflight")
					}
					sql = sql[start:]
					sql = strings.ReplaceAll(sql, "job_title_is_blank(j.job_type)", "btrim(j.job_type)=''")
					sql = strings.ReplaceAll(sql, "job_title_is_blank(NEW.job_type)", "btrim(NEW.job_type)=''")
					data = []byte(sql)
				}
				old[entry.Name()] = &fstest.MapFile{Data: data}
			}
			if err := db.Migrate(ctx, old); err != nil {
				t.Fatal(err)
			}
			var contact, id int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Retained Contact') RETURNING id`, a.company, a.customer), &contact)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,customer_contact_id) VALUES($1,$2,$3,$4) RETURNING id`, a.company, a.customer, title, contact), &id)
			var before, after string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &before)
			err = db.Migrate(ctx, MigrationFS())
			if strings.TrimSpace(title) == "" {
				if err == nil || !strings.Contains(err.Error(), "Administrator review") {
					t.Fatalf("forward review gate: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &after)
			if before != after {
				t.Fatal("forward migration changed historical facts")
			}
			var retained, applied bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_contacts WHERE job_id=$1 AND contact_id=$2),EXISTS(SELECT 1 FROM schema_migrations WHERE name='075_job_title_whitespace')`, id, contact), &retained, &applied)
			if !retained || applied != (strings.TrimSpace(title) != "") {
				t.Fatal("forward migration lost membership or published rejected cutover")
			}
			if applied {
				if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET job_type=E'\t\r\n' WHERE id=$1`, id); err == nil {
					t.Fatal("forward invariant accepted whitespace-only title")
				}
			}
		})
	}
}
