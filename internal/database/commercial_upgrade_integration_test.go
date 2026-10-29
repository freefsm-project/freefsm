package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/testutil"
)

func TestCommercialUpgradePreservesFactsAndRepairsFutureSequence(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "review-required"}[invalid], func(t *testing.T) {
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "commercial-upgrade")
			prior := fstest.MapFS{}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() >= "076_" {
					continue
				}
				data, err := fs.ReadFile(MigrationFS(), entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				prior[entry.Name()] = &fstest.MapFile{Data: data}
			}
			if err := db.Migrate(ctx, prior); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,next_invoice_number,next_estimate_number) VALUES($1,400,900)`, a.company); err != nil {
				t.Fatal(err)
			}
			var job int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Recorded Job') RETURNING id`, a.company, a.customer), &job)
			for _, kind := range []string{"estimate", "invoice"} {
				extra, values := "", ""
				if kind == "invoice" {
					extra = ",invoice_number"
					values = ",400"
				}
				if _, err := db.Pool.Exec(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,job_id,title,notes,terms,line_items`+extra+`) VALUES($1,$2,$3,'Recorded title','Recorded note','Recorded terms','[{"title":"Historical snapshot","quantity":2,"unit_price":17.25}]'`+values+`)`, a.company, a.customer, job); err != nil {
					t.Fatal(err)
				}
			}
			if invalid {
				if _, err := db.Pool.Exec(ctx, `UPDATE estimates SET title=E'\t\n'`); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately reproduce an older externally lowered allocator without
			// changing any issued identity. The upgrade must repair, not renumber.
			if _, err := db.Pool.Exec(ctx, `ALTER TABLE company_settings DISABLE TRIGGER USER; UPDATE company_settings SET next_invoice_number=1,next_estimate_number=1; ALTER TABLE company_settings ENABLE TRIGGER USER`); err != nil {
				t.Fatal(err)
			}
			var before, after string
			query := `SELECT jsonb_build_object('estimates',(SELECT jsonb_agg((to_jsonb(e)-ARRAY['commercial_revision','financial'])||jsonb_build_object('tax_rate',e.tax_rate::text) ORDER BY id) FROM estimates e),'invoices',(SELECT jsonb_agg((to_jsonb(i)-ARRAY['commercial_revision','financial','lease_id'])||jsonb_build_object('tax_rate',i.tax_rate::text) ORDER BY id) FROM invoices i),'issued',(SELECT jsonb_agg(n ORDER BY document_type,number) FROM issued_document_numbers n))::text`
			mustMigrationScan(t, db.Pool.QueryRow(ctx, query), &before)
			sequences := testutil.Sequences(t, ctx, db.Pool)
			err = db.Migrate(ctx, MigrationFS())
			if invalid {
				if err == nil || !strings.Contains(err.Error(), "Administrator review") {
					t.Fatalf("ambiguous upgrade=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, query), &after)
			added := []string{"asset_association_events_id_seq", "job_asset_links_id_seq", "subtask_history_id_seq"}
			if !invalid {
				testutil.AssertNoInvoiceLeaseBackfill(t, ctx, db.Pool)
				added = append(added, "asset_service_entries_id_seq", "lease_billing_obligations_id_seq", "leases_id_seq", "time_entry_history_id_seq", "lease_billing_source_changes_id_seq", "lease_billing_obligation_revisions_id_seq")
			}
			testutil.AssertSequenceUpgrade(t, ctx, db.Pool, sequences, added...)
			if before != after {
				t.Fatal("commercial upgrade rewrote recorded facts")
			}
			if !invalid {
				var repaired bool
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT next_invoice_number=401 AND next_estimate_number=901 FROM company_settings WHERE company_id=$1`, a.company), &repaired)
				if !repaired {
					t.Fatal("future allocator can reuse a known number")
				}
			}
		})
	}
}
