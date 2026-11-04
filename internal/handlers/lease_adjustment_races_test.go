package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"testing"
	"time"
)

func TestLeaseAdjustmentSettlementCommitOrders(t *testing.T) {
	for _, adjustFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(adjustFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
			var invoice, asset int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
			historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice))
			svc := authorization.New(pool)
			// The office receipt service requires the caller's channel for its
			// immutable audit, just as the production browser handler supplies it.
			actor := authorization.Actor{UserID: admin, Channel: "web"}
			review, err := svc.ReviewLeaseAdjustments(ctx, actor, lease, invoice)
			historyCheck(t, err)
			adjust := func() error {
				return svc.ApplyLeaseAdjustment(ctx, actor, lease, authorization.LeaseAdjustmentInput{InvoiceID: invoice, AssetID: asset, InvoiceRevision: review.Selected.Revision, Kind: "abatement", AmountCents: 1, StartsOn: "2024-01-01", EndsOn: "2024-01-01", Reason: "Explicit reduction during receipt contention", OperationKey: "adjustment-settlement-race-0001"})
			}
			pay := func() error {
				_, e := settlement.New(pool).RecordInvoicePaymentWithCredit(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "72020100-0000-4000-8000-000000000001"}, InvoiceID: invoice, AmountCents: 50, Method: settlement.Cash, ReceivedDate: "2024-01-01"})
				return e
			}
			first, second := pay, adjust
			action := "payment.credit"
			total := "106"
			applications := 0
			if adjustFirst {
				first, second = adjust, pay
				action = "lease.billing.adjust"
				total = "105"
				applications = 1
			}
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION adjustment_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN PERFORM pg_advisory_xact_lock(720201); END IF; RETURN NEW; END $$; CREATE TRIGGER adjustment_pause AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION adjustment_pause()`, action))
			historyCheck(t, err)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(720201)`).Scan(&holder))
			one, two := make(chan error, 1), make(chan error, 1)
			go func() { one <- first() }()
			commercialWaiters(t, ctx, pool, holder, 1)
			var writer int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
			go func() { two <- second() }()
			commercialWaiters(t, ctx, pool, writer, 1)
			historyCheck(t, barrier.Commit(ctx))
			historyCheck(t, <-one)
			err = <-two
			if adjustFirst {
				historyCheck(t, err)
			} else if !errors.Is(err, authorization.ErrConflict) {
				t.Fatalf("settlement must reject queued commercial reduction as a domain conflict: %v", err)
			}
			var actual string
			var count int
			var settled int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT financial::jsonb->'result'->>'total_cents' FROM invoices WHERE id=$1`, invoice).Scan(&actual))
			historyEqual(t, "commit-order total", actual, total)
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments WHERE lease_id=$1`, lease).Scan(&count))
			historyEqual(t, "all or zero application", count, applications)
			historyCheck(t, pool.QueryRow(ctx, `SELECT sum(amount_cents) FROM payment_invoice_allocations WHERE invoice_id=$1`, invoice).Scan(&settled))
			historyEqual(t, "valid receipt conserved", settled, int64(50))
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='payment.credit' AND actor_id=$1 AND channel='web' AND target_type='invoice' AND target_id=$2`, admin, invoice).Scan(&count))
			historyEqual(t, "receipt commits with attributable product audit", count, 1)
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='lease.billing.adjust' AND actor_id=$1 AND channel='web' AND target_id=$2`, admin, lease).Scan(&count))
			historyEqual(t, "adjustment audit commits with its effect only", count, applications)
		})
	}
}
