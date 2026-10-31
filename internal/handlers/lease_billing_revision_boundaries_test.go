package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestLeaseBillingRevisionGeneratedExceptions(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-01-01")
	lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	clock("2024-06-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	invoice := func(due string) int64 {
		t.Helper()
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT g.invoice_id FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on=$2::date`, lease, due).Scan(&id))
		return id
	}
	flow := statusflow.New(pool)
	fa := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
	finalized, voided, paid := invoice("2024-02-01"), invoice("2024-03-01"), invoice("2024-04-01")
	for _, id := range []int64{finalized, voided, paid} {
		historyCheck(t, flow.FinalizeInvoice(ctx, fa, id))
	}
	var void int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
	historyCheck(t, flow.TransitionInvoice(ctx, fa, voided, void))
	_, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "billing-revision-paid-original"}, InvoiceID: paid, AmountCents: 5250, Method: settlement.Cash, ReceivedDate: "2024-04-01"})
	historyCheck(t, err)
	tables := []string{"invoices", "lease_finalizations", "lease_invoice_generations", "invoice_payments", "payment_invoice_allocations"}
	original := historyDurableRows(t, pool, tables)
	in := authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-02-01", Reason: "Revised agreement after generated bills", OperationKey: "billing-generated-revision-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 7000, TaxRate: "5"}}}
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, in))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "Draft finalized Void settled originals unchanged", historyDurableRows(t, pool, tables), original)
	var flags int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(DISTINCT r.invoice_id) FROM lease_invoice_review_flags r JOIN lease_invoice_generations g ON g.invoice_id=r.invoice_id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1`, lease).Scan(&flags))
	historyEqual(t, "each affected monthly invoice flagged", flags, 6)
	clock("2024-06-10")
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-06-10", EffectiveOn: "2024-06-30", Reason: "Agreement ended", OperationKey: "billing-july-exception-end-0001"}))
	july := invoice("2024-07-01")
	var flagged bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_review_flags WHERE invoice_id=$1)`, july).Scan(&flagged))
	historyEqual(t, "July Draft exception", flagged, true)
	clock("2024-08-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "unowed months and void do not regenerate", historyDurableRows(t, pool, tables), original)
	historyCheck(t, svc.LeaseLifecycle(ctx, actor, lease, false))
	review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
	historyCheck(t, err)
	in.ExpectedRevision = review.Revision
	in.OperationKey = "billing-archived-denial-0001"
	if err = svc.ReviseLeaseBilling(ctx, actor, lease, in); err == nil {
		t.Fatal("archived revision accepted")
	}
}

func TestLeaseBillingRevisionGenerationCommitOrders(t *testing.T) {
	for _, firstRevision := range []bool{true, false} {
		t.Run(fmt.Sprint(firstRevision), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			svc := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
			historyCheck(t, err)
			lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
			var asset int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
			_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$`)
			historyCheck(t, err)
			in := authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-02-01", Reason: "Concurrent approved rate", OperationKey: "billing-generation-race-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 6000, TaxRate: "5"}}}
			revise := func() error { return svc.ReviseLeaseBilling(ctx, actor, lease, in) }
			generate := func() error { return svc.GenerateLeaseDrafts(ctx) }
			first, second := generate, revise
			action := "lease.invoice.generate"
			expected := "5250"
			if firstRevision {
				first, second = revise, generate
				action = "lease.billing.revise"
				expected = "6300"
			}
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION billing_revision_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN PERFORM pg_advisory_xact_lock(710201); END IF; RETURN NEW; END $$; CREATE TRIGGER billing_revision_pause AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION billing_revision_pause()`, action))
			historyCheck(t, err)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(710201)`).Scan(&holder))
			one, two := make(chan error, 1), make(chan error, 1)
			go func() { one <- first() }()
			commercialWaiters(t, ctx, pool, holder, 1)
			var writer int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
			go func() { two <- second() }()
			commercialWaiters(t, ctx, pool, writer, 1)
			historyCheck(t, barrier.Commit(ctx))
			historyCheck(t, <-one)
			historyCheck(t, <-two)
			historyCheck(t, revise())
			historyCheck(t, generate())
			var amount string
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*),min(i.financial::jsonb->'result'->>'total_cents') FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-02-01'`, lease).Scan(&count, &amount))
			historyEqual(t, "single coherent generation", count, 1)
			historyEqual(t, "commit order controls terms", amount, expected)
		})
	}
}
