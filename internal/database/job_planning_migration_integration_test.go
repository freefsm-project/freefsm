package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestJobPlanningUpgradePreservesOrStopsWithoutInventingFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "review-required"}[invalid], func(t *testing.T) {
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "job-history")
			old := fstest.MapFS{}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() >= "074_" {
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
			title := "Recorded work"
			if invalid {
				title = ""
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,customer_contact_id) VALUES($1,$2,$3,$4) RETURNING id`, a.company, a.customer, title, contact), &id)
			var before, after string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &before)
			err = db.Migrate(ctx, MigrationFS())
			if invalid {
				if err == nil || !strings.Contains(err.Error(), "Administrator review") {
					t.Fatalf("preflight=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &after)
			if before != after {
				t.Fatal("upgrade altered recorded Job facts")
			}
			if invalid {
				var absent bool
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_regclass('job_contacts') IS NULL`), &absent)
				if !absent {
					t.Fatal("failed preflight partially migrated")
				}
			} else {
				var linked bool
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_contacts WHERE job_id=$1 AND contact_id=$2)`, id, contact), &linked)
				if !linked {
					t.Fatal("recorded Contact link lost")
				}
			}
		})
	}
}
