package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationGuardCheckpoint(t *testing.T, before string) fs.FS {
	t.Helper()
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() >= before {
			continue
		}
		data, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return out
}

func integrationGuardExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// Exercise a genuine 059 -> 067 -> 068 upgrade with active and reverted
// conversion history. The command, reservation, immutable audit and creation
// gate are production code; no trigger is disabled to manufacture the fixture.
func TestIntegrationMigrationGuardsLegacyConversionReview(t *testing.T) {
	ctx := context.Background()
	pool, _, _, _, company, admin := permissionHTTPFixtureMigrations(t, integrationGuardCheckpoint(t, "060_"))
	workflow := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	oldStatus, err := workflow.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Legacy accepted", Category: statusflow.EstimateAccepted})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := workflow.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Replacement accepted", Category: statusflow.EstimateAccepted})
	if err != nil {
		t.Fatal(err)
	}
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Legacy conversions') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	var estimates, invoices [2]int64
	for i := range estimates {
		if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title,status_id) VALUES($1,$2,'Recorded estimate',$3) RETURNING id`, company, customer, oldStatus.ID).Scan(&estimates[i]); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title) VALUES($1,$2,$3,'Recorded invoice') RETURNING id`, company, customer, 70+i).Scan(&invoices[i]); err != nil {
			t.Fatal(err)
		}
		integrationGuardExec(t, pool, `INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,invoice_number,source_snapshot,converted_by) VALUES($1,$2,$3,$4,$5,'{}',$6)`, fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), company, estimates[i], invoices[i], 70+i, admin)
		integrationGuardExec(t, pool, `UPDATE estimates SET conversion_hidden_at=now() WHERE id=$1`, estimates[i])
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.conversion_revert_cycle','00000000-0000-0000-0000-000000000002',true); UPDATE estimate_invoice_conversion_cycles SET reverted_at=now(),reverted_by=converted_by WHERE id='00000000-0000-0000-0000-000000000002'; UPDATE invoices SET conversion_hidden_at=now() WHERE id=(SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE id='00000000-0000-0000-0000-000000000002'); UPDATE estimates SET conversion_hidden_at=NULL WHERE id=(SELECT estimate_id FROM estimate_invoice_conversion_cycles WHERE id='00000000-0000-0000-0000-000000000002')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	db := &database.DB{Pool: pool}
	if err = db.Migrate(ctx, integrationGuardCheckpoint(t, "068_")); err != nil {
		t.Fatal(err)
	}
	svc := authorization.New(pool)
	review := func(kind string, id, number int64) error {
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM company_settings WHERE company_id=$1`, company).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return svc.ReviewLegacyNumber(ctx, authorization.Actor{UserID: admin}, revision, kind, id, number, 100, fmt.Sprintf("REVIEWED-%s-%d", kind, number))
	}
	for _, target := range []struct {
		kind       string
		id, number int64
	}{{"estimate", estimates[0], 70}, {"invoice", invoices[1], 71}} {
		if err = review(target.kind, target.id, target.number); err == nil || !strings.Contains(err.Error(), "conversion tombstone is immutable") {
			t.Fatalf("old checkpoint review = %v", err)
		}
	}
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		kind       string
		id, number int64
	}{{"estimate", estimates[0], 70}, {"invoice", invoices[1], 71}} {
		// Neither the setting alone nor a reservation plus unrelated edits may
		// turn review into a general tombstone write capability.
		integrationGuardReject(t, pool, "freefsm.legacy_number_review", `UPDATE `+target.kind+`s SET number_label='UNRESERVED' WHERE id=$1`, target.id)
		for _, change := range []string{"title='tampered'", "deleted_at=now()", "conversion_hidden_at=NULL", "status_revision=gen_random_uuid()"} {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.legacy_number_review','allowed',true)`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO issued_document_numbers VALUES($1,$2,$3,'RESERVED')`, company, target.kind, target.number); err != nil {
				t.Fatal(err)
			}
			identity := "number_label='RESERVED'"
			if target.kind == "estimate" {
				identity += fmt.Sprintf(",document_number=%d", target.number)
			}
			_, err = tx.Exec(ctx, `UPDATE `+target.kind+`s SET `+identity+`,`+change+` WHERE id=$1`, target.id)
			tx.Rollback(ctx)
			if err == nil {
				t.Fatalf("review accepted mixed mutation %s", change)
			}
		}
		if _, err = pool.Exec(ctx, `INSERT INTO `+target.kind+`s(company_id,customer_id,title) VALUES($1,$2,'Still blocked')`, company, customer); err == nil || !strings.Contains(err.Error(), "Legacy numbering requires review") {
			t.Fatalf("unreviewed creation gate = %v", err)
		}
	}
	// Audit failure must roll back the identity exception and its reservation.
	integrationGuardExec(t, pool, `CREATE FUNCTION reject_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected review audit failure'; END $$; CREATE TRIGGER reject_review_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_review_audit()`)
	if err = review("invoice", invoices[1], 71); err == nil || !strings.Contains(err.Error(), "injected review audit failure") {
		t.Fatalf("audit failure = %v", err)
	}
	var reserved int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM issued_document_numbers`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("partial reservation: %d, %v", reserved, err)
	}
	integrationGuardExec(t, pool, `DROP TRIGGER reject_review_audit ON authorization_audit`)
	conversionFacts := func() string {
		t.Helper()
		var snapshot string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		  'estimates',(SELECT jsonb_agg(to_jsonb(e)-ARRAY['number_label','document_number','commercial_revision'] ORDER BY id) FROM estimates e),
		  'invoices',(SELECT jsonb_agg(to_jsonb(i)-ARRAY['number_label','commercial_revision'] ORDER BY id) FROM invoices i),
		  'cycles',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM estimate_invoice_conversion_cycles c))::text`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	beforeReview := conversionFacts()
	for i := range estimates {
		if err = review("estimate", estimates[i], int64(70+i)); err != nil {
			t.Fatal(err)
		}
		if err = review("invoice", invoices[i], int64(70+i)); err != nil {
			t.Fatal(err)
		}
	}
	if afterReview := conversionFacts(); beforeReview != afterReview {
		t.Fatalf("review rewrote conversion facts: %s -> %s", beforeReview, afterReview)
	}
	// Preserve the pre-existing exact status replacement exception after review,
	// including the deliberately later revision trigger.
	var oldRevision, newRevision string
	if err = pool.QueryRow(ctx, `SELECT status_revision::text FROM estimates WHERE id=$1`, estimates[0]).Scan(&oldRevision); err != nil {
		t.Fatal(err)
	}
	integrationGuardReject(t, pool, "freefsm.status_replacement", `UPDATE estimates SET status_id=$2,title='tampered' WHERE id=$1`, estimates[0], replacement.ID)
	if err = workflow.Delete(ctx, actor, oldStatus.ID, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT status_revision::text FROM estimates WHERE id=$1`, estimates[0]).Scan(&newRevision); err != nil || oldRevision == newRevision {
		t.Fatalf("replacement did not revise workflow intent: %v", err)
	}
	if err = review("invoice", invoices[1], 71); err == nil {
		t.Fatal("repeat review accepted")
	}
	for _, kind := range []string{"estimate", "invoice"} {
		var label string
		if err = pool.QueryRow(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,title) VALUES($1,$2,'After review') RETURNING number_label`, company, customer).Scan(&label); err != nil || label == "" {
			t.Fatalf("%s gate remains blocked: %q %v", kind, label, err)
		}
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE detail->>'legacy_number_review'='true'`).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("review audit count %d: %v", audits, err)
	}
	for _, target := range []struct {
		table string
		id    int64
	}{{"estimates", estimates[0]}, {"invoices", invoices[1]}} {
		for _, change := range []string{"title='tampered'", "number_label='rewritten'", "conversion_hidden_at=NULL", "status_revision=gen_random_uuid()"} {
			integrationGuardReject(t, pool, "freefsm.legacy_number_review", `UPDATE `+target.table+` SET `+change+` WHERE id=$1`, target.id)
		}
		integrationGuardReject(t, pool, "freefsm.legacy_number_review", `DELETE FROM `+target.table+` WHERE id=$1`, target.id)
	}
}

func integrationGuardReject(t *testing.T, pool *pgxpool.Pool, setting, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config($1,'allowed',true)`, setting); err != nil {
		t.Fatal(err)
	}
	if setting == "freefsm.status_replacement" {
		if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, query, args...); err == nil {
		t.Fatalf("guard accepted %s", query)
	} else if !strings.Contains(err.Error(), "conversion tombstone is immutable") && !strings.Contains(err.Error(), "estimate restore requires reverted conversion cycle") && !strings.Contains(err.Error(), "Select an active") {
		t.Fatalf("unexpected rejection for %s: %v", query, err)
	}
}

func TestIntegrationMigrationGuardsArchivedMasterReplacement(t *testing.T) {
	ctx := context.Background()
	pool, _, _, _, company, admin := permissionHTTPFixtureMigrations(t, integrationGuardCheckpoint(t, "068_"))
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	for _, kind := range []statusflow.ObjectType{statusflow.Job, statusflow.Project} {
		t.Run(string(kind), func(t *testing.T) {
			old, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: kind, Name: "Historical pending", Category: statusflow.CategoryKey(string(kind) + ":pending")})
			if err != nil {
				t.Fatal(err)
			}
			next, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: kind, Name: "Replacement pending", Category: old.Category})
			if err != nil {
				t.Fatal(err)
			}
			var customer, location, contact, work int64
			if err = pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Historical owner') RETURNING id`, company).Scan(&customer); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, `INSERT INTO locations(company_id,object_type,object_id,title) VALUES($1,'customer',$2,'Historical site') RETURNING id`, company, customer).Scan(&location); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Historical person') RETURNING id`, company, customer).Scan(&contact); err != nil {
				t.Fatal(err)
			}
			table := string(kind) + "s"
			columns, values := "name", "'Historical project'"
			if kind == statusflow.Job {
				columns, values = "job_type,customer_contact_id", fmt.Sprintf("'Historical job',%d", contact)
			}
			if err = pool.QueryRow(ctx, `INSERT INTO `+table+`(company_id,customer_id,location_id,status_id,`+columns+`) VALUES($1,$2,$3,$4,`+values+`) RETURNING id`, company, customer, location, old.ID).Scan(&work); err != nil {
				t.Fatal(err)
			}
			integrationGuardExec(t, pool, `UPDATE `+table+` SET deleted_at=now() WHERE id=$1`, work)
			integrationGuardExec(t, pool, `UPDATE locations SET deleted_at=now() WHERE id=$1`, location)
			integrationGuardExec(t, pool, `UPDATE customer_contacts SET deleted_at=now() WHERE id=$1`, contact)
			integrationGuardExec(t, pool, `UPDATE customers SET deleted_at=now() WHERE id=$1`, customer)
			if kind == statusflow.Job {
				if err = svc.Delete(ctx, actor, old.ID, next.ID); err == nil || !strings.Contains(err.Error(), "active Location") {
					t.Fatalf("old checkpoint replacement = %v", err)
				}
				if err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			if err = pool.QueryRow(ctx, `SELECT (to_jsonb(w)-ARRAY['status_id','status_revision'])::text FROM `+table+` w WHERE id=$1`, work).Scan(&before); err != nil {
				t.Fatal(err)
			}
			// A genuine restoration fails even with the replacement setting.
			integrationGuardReject(t, pool, "freefsm.status_replacement", `UPDATE `+table+` SET status_id=$2,deleted_at=NULL WHERE id=$1`, work, next.ID)
			var otherLocation int64
			if err = pool.QueryRow(ctx, `INSERT INTO locations(company_id,object_type,object_id,title,deleted_at) VALUES($1,'customer',$2,'Other archived site',now()) RETURNING id`, company, customer).Scan(&otherLocation); err != nil {
				t.Fatal(err)
			}
			integrationGuardReject(t, pool, "freefsm.status_replacement", `UPDATE `+table+` SET status_id=$2,location_id=$3 WHERE id=$1`, work, next.ID, otherLocation)
			integrationGuardReject(t, pool, "freefsm.status_replacement", `UPDATE `+table+` SET status_id=(SELECT id FROM statuses WHERE company_id=$2 AND category_key=$3 LIMIT 1) WHERE id=$1`, work, company, string(kind)+":new")
			if err = svc.Delete(ctx, actor, old.ID, next.ID); err != nil {
				t.Fatal(err)
			}
			var status int64
			if err = pool.QueryRow(ctx, `SELECT status_id,(to_jsonb(w)-ARRAY['status_id','status_revision'])::text FROM `+table+` w WHERE id=$1`, work).Scan(&status, &after); err != nil {
				t.Fatal(err)
			}
			if status != next.ID || before != after {
				t.Fatalf("replacement changed historical facts: %d %s -> %s", status, before, after)
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='zz_status_revision'`, pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil || count != 1 {
				t.Fatalf("late revision trigger missing: %v", err)
			}
		})
	}
}
