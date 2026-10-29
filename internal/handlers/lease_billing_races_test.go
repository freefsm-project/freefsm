package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

// The SQL trigger is an explicit test barrier at the actual writer's audit, not
// fixture-generated production coverage. PostgreSQL blocking edges establish
// the order; wall-clock sleeps do not stand in for observed contention.
func TestLeaseBillingWorkerCommitRaces(t *testing.T) {
	for _, kind := range []string{"worker", "retry", "retry-first", "archive", "placement", "timeline", "replace-reactivate", "reactivate-replace"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			svc := authorization.New(pool)
			cookie := sessionCookie(t, ctx, sessions, admin)
			_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
			historyCheck(t, err)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race billing").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
			l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Covered").SaveX(ctx)
			var event int64
			if kind == "timeline" {
				fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/associations/place", asset.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {"2024-01-01"}, "reason": {"Original placement"}, "revision": {"0"}}, 303)
				fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/associations/return", asset.ID), url.Values{"effective_date": {"2024-01-15"}, "reason": {"Return"}, "revision": {"1"}}, 303)
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='place'`, asset.ID).Scan(&event))
			}
			fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
			var lease int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&lease))
			path := fmt.Sprintf("/leases/%d", lease)
			fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-12-31"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}}, 303)
			amount := int64(100)
			historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, lease, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
			fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 303)
			first := func() error { return svc.GenerateLeaseDrafts(ctx) }
			second := first
			action := "lease.invoice.generate"
			if kind == "replace-reactivate" || kind == "reactivate-replace" {
				var invoice, obligation, void, active int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT g.invoice_id,g.obligation_id FROM lease_invoice_generations g`).Scan(&invoice, &obligation))
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced' AND is_category_default`, company).Scan(&active))
				flow := statusflow.New(pool)
				actor := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
				historyCheck(t, flow.FinalizeInvoice(ctx, actor, invoice))
				historyCheck(t, flow.TransitionInvoice(ctx, actor, invoice, void))
				review, err := svc.ReviewLeaseBillingPeriod(ctx, authorization.Actor{UserID: admin}, lease, obligation)
				historyCheck(t, err)
				first = func() error {
					_, err := svc.ReplaceLeaseInvoice(ctx, authorization.Actor{UserID: admin}, lease, obligation, authorization.LeaseReplacementInput{InvoiceID: invoice, ReviewHash: review.ReviewHash, OperationKey: review.OperationKey})
					return err
				}
				second = func() error { return flow.TransitionInvoice(ctx, actor, invoice, active) }
				if kind == "reactivate-replace" {
					first, second = second, first
					action = "invoice.status"
				}
			} else {
				_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-04-01'::date $$`)
				historyCheck(t, err)
				command := creditCommand{path: fmt.Sprintf("/assets/%d/delete", asset.ID)}
				if kind == "placement" {
					command = creditCommand{path: fmt.Sprintf("/assets/%d/associations/place", asset.ID), values: url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {"2024-04-01"}, "reason": {"Competing placement"}, "revision": {"0"}}}
				}
				if kind == "timeline" {
					command = creditCommand{path: fmt.Sprintf("/assets/%d/associations/correct", asset.ID), values: url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {"2024-01-01"}, "event_id": {fmt.Sprint(event)}, "reason": {"Competing historical correction"}, "revision": {"2"}}}
				}
				if kind != "worker" && kind != "retry" && kind != "retry-first" {
					second = func() error {
						r := (&creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}).call(command)
						if r.Code == 303 {
							return nil
						}
						if r.Code != 409 {
							return fmt.Errorf("unexpected HTTP status %d: %s", r.Code, r.Body.String())
						}
						return authorization.ErrConflict
					}
				}
				if kind == "retry" || kind == "retry-first" {
					var obligation int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, lease).Scan(&obligation))
					review, err := svc.ReviewLeaseBillingPeriod(ctx, authorization.Actor{UserID: admin}, lease, obligation)
					historyCheck(t, err)
					second = func() error {
						_, err := svc.RetryLeaseBillingPeriod(ctx, authorization.Actor{UserID: admin}, lease, obligation, review.ReviewHash, review.OperationKey)
						return err
					}
					if kind == "retry-first" {
						first, second = second, first
					}
				}
			}
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION billing_race_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN PERFORM pg_advisory_xact_lock(670201); END IF; RETURN NEW; END $$; CREATE TRIGGER billing_race_pause AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION billing_race_pause()`, action))
			historyCheck(t, err)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(670201)`).Scan(&holder))
			done1, done2 := make(chan error, 1), make(chan error, 1)
			go func() { done1 <- first() }()
			commercialWaiters(t, ctx, pool, holder, 1)
			var writer int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
			if kind != "replace-reactivate" && kind != "reactivate-replace" && kind != "retry-first" {
				var obligations, generated int
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_billing_obligations),(SELECT count(*) FROM lease_invoice_generations)`).Scan(&obligations, &generated))
				historyEqual(t, "materialization committed before paused Invoice attempt", obligations, 5)
				historyEqual(t, "no uncommitted success visible", generated, 1)
			}
			go func() { done2 <- second() }()
			commercialWaiters(t, ctx, pool, writer, 1)
			historyCheck(t, barrier.Commit(ctx))
			historyCheck(t, <-done1)
			secondErr := <-done2
			if kind == "worker" || kind == "retry-first" {
				historyCheck(t, secondErr)
			} else if secondErr == nil {
				t.Fatal("competing ineligible effect committed")
			}
			if (kind == "archive" || kind == "placement" || kind == "timeline" || kind == "reactivate-replace" || kind == "retry") && !errors.Is(secondErr, authorization.ErrConflict) {
				t.Fatalf("unexpected competitor failure: %v", secondErr)
			}
			var competing bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT g.obligation_id FROM lease_invoice_generations g JOIN invoices i ON i.id=g.invoice_id JOIN statuses s ON s.id=i.status_id WHERE s.category_key<>'invoice:void' GROUP BY g.obligation_id HAVING count(*)>1)`).Scan(&competing))
			historyEqual(t, "one active generation per period", competing, false)
		})
	}
}
