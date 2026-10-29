package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Pause the actual native writer at its last audit, then observe the competing
// backend waiting on that writer's fence. No sleeps establish the commit order.
func TestLeaseFinalizationNativeRacesHTTP(t *testing.T) {
	for _, kind := range []string{"finalization", "asset", "location", "customer", "placement", "asset-first", "placement-first", "premises", "premises-first", "billing", "billing-first", "replay", "correction", "correction-first", "invoice-content"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race Customer").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
			l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Race Asset").SaveX(ctx)
			var today time.Time
			historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
			date := today.Format(time.DateOnly)
			var event int64
			if kind == "correction" || kind == "correction-first" {
				date = today.AddDate(0, 0, -20).Format(time.DateOnly)
				assetPath := fmt.Sprintf("/assets/%d/associations/", a.ID)
				fieldPost(t, web, cookie, assetPath+"place", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {date}, "reason": {"Original placement"}, "revision": {"0"}}, 303)
				fieldPost(t, web, cookie, assetPath+"return", url.Values{"effective_date": {today.AddDate(0, 0, -10).Format(time.DateOnly)}, "reason": {"Return"}, "revision": {"1"}}, 303)
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='place'`, a.ID).Scan(&event))
			}
			values := url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {date}}
			create := func() int64 {
				fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
				var id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM leases`).Scan(&id))
				fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d", id), url.Values{"revision": {"0"}, "starts_on": {date}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
				amount := int64(100)
				historyCheck(t, authorization.New(pool).SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
				return id
			}
			id := create()
			competitor := creditCommand{path: fmt.Sprintf("/assets/%d/delete", a.ID)}
			switch kind {
			case "finalization":
				competitor = creditCommand{path: fmt.Sprintf("/leases/%d/finalize", create()), values: values}
			case "location":
				competitor.path = fmt.Sprintf("/locations/%d/delete", l.ID)
			case "customer":
				var preview authorization.CustomerArchivePreview
				masterJSON(t, web, cookie, fmt.Sprintf("/customers/%d/archive", c.ID), &preview)
				competitor = creditCommand{path: fmt.Sprintf("/customers/%d/delete", c.ID), values: url.Values{"archive_snapshot": {preview.Snapshot}}}
			case "placement", "placement-first":
				competitor = creditCommand{path: fmt.Sprintf("/assets/%d/associations/place", a.ID), values: url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {date}, "reason": {"Competing placement"}, "revision": {"0"}}}
			case "premises", "premises-first":
				competitor = creditCommand{path: fmt.Sprintf("/leases/%d", id), values: url.Values{"revision": {"1"}, "starts_on": {date}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "asset_ids": {fmt.Sprint(a.ID)}}}
			case "billing", "billing-first":
				competitor = creditCommand{path: fmt.Sprintf("/leases/%d/billing", id), values: url.Values{"revision": {"1"}, "lifecycle_revision": {"0"}, "asset_id": {fmt.Sprint(a.ID)}, "initial_due_on": {date}, "initial_cents": {"200"}, "rent_cents": {"200"}, "tax_rate": {"0"}}}
			case "replay":
				competitor = creditCommand{path: fmt.Sprintf("/leases/%d/finalize", id), values: values}
			case "correction", "correction-first":
				competitor = creditCommand{path: fmt.Sprintf("/assets/%d/associations/correct", a.ID), values: url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {date}, "event_id": {fmt.Sprint(event)}, "reason": {"Correct historical Customer"}, "revision": {"2"}}}
			}
			command := creditCommand{path: fmt.Sprintf("/leases/%d/finalize", id), values: values}
			action := "lease.finalize"
			reverse := kind == "asset-first" || kind == "placement-first" || kind == "premises-first" || kind == "billing-first" || kind == "correction-first"
			if reverse {
				command, competitor = competitor, command
				action = "asset.archive"
				if kind == "placement-first" {
					action = "asset.place"
				}
				if kind == "premises-first" {
					action = "lease.details.change"
				}
				if kind == "billing-first" {
					action = "lease.billing.change"
				}
				if kind == "correction-first" {
					action = "asset.history.correct"
				}
			}
			if kind == "invoice-content" {
				fieldPost(t, web, cookie, command.path, command.values, 303)
				competitor = command // generation replay waits on the ordinary Invoice writer
				var invoice, number int64
				var lines string
				historyCheck(t, pool.QueryRow(ctx, `SELECT id,invoice_number,line_items::text FROM invoices ORDER BY id LIMIT 1`).Scan(&invoice, &number, &lines))
				invoicePath := fmt.Sprintf("/invoices/%d", invoice)
				form := commercialReviewedValues(t, web, cookie, invoicePath, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "invoice_number": {fmt.Sprint(number)}, "title": {"Explicit ordinary Invoice edit"}, "line_items": {lines}, "tax_rate": {"0"}, "customer_defaults_reviewed": {"true"}})
				command = creditCommand{path: invoicePath, values: form}
				action = "invoice.edit"
			}
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE race_expected (state jsonb);
CREATE FUNCTION race_state() RETURNS jsonb LANGUAGE plpgsql AS $$ DECLARE result jsonb:='{}'; name text; value jsonb; BEGIN
 FOREACH name IN ARRAY ARRAY['leases','lease_intended_assets','lease_draft_billing','lease_draft_asset_terms','lease_master_dependencies','lease_asset_dependencies','lease_finalizations','lease_billing_obligations','lease_invoice_generations','invoices','company_settings','issued_document_numbers','authorization_audit','assets','locations','customers','asset_association_events','asset_association_origins','asset_association_origin_revisions','activity_logs'] LOOP
 EXECUTE format('SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),''[]''::jsonb) FROM %%I t',name) INTO value;
 result:=result||jsonb_build_object(name,value); END LOOP; RETURN result; END $$;
CREATE FUNCTION pause_finalization() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN INSERT INTO race_expected VALUES(race_state()); PERFORM pg_advisory_xact_lock(660201); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_finalization AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_finalization()`, action))
			historyCheck(t, err)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(660201)`).Scan(&holder))
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			first, second := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- f.call(command) }()
			commercialWaiters(t, ctx, pool, holder, 1)
			var writer int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
			go func() { second <- f.call(competitor) }()
			commercialWaiters(t, ctx, pool, writer, 1)
			historyCheck(t, barrier.Commit(ctx))
			if r := <-first; r.Code != 303 {
				t.Fatalf("finalization: %d %s", r.Code, r.Body.String())
			}
			want := 409
			if kind == "replay" || kind == "invoice-content" {
				want = 303
			}
			if r := <-second; r.Code != want {
				t.Fatalf("competitor: %d %s", r.Code, r.Body.String())
			}
			var valid bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT race_state()=(SELECT state FROM race_expected)`).Scan(&valid))
			if !valid {
				t.Fatal("competing writer changed whole committed winner state")
			}
			if reverse {
				historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_finalizations) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations) AND NOT EXISTS(SELECT 1 FROM invoices) AND NOT EXISTS(SELECT 1 FROM issued_document_numbers WHERE document_type='invoice') AND EXISTS(SELECT 1 FROM leases WHERE id=$1 AND lifecycle='draft')`, id).Scan(&valid))
			} else {
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_finalizations)=1 AND (SELECT count(*) FROM lease_asset_dependencies)=1 AND NOT EXISTS(SELECT 1 FROM invoices WHERE lease_id<>$1) AND EXISTS(SELECT 1 FROM assets WHERE id=$2 AND customer_id IS NULL AND deleted_at IS NULL) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations o WHERE lease_id<>$1)`, id, a.ID).Scan(&valid))
			}
			if !valid {
				t.Fatal("race left partial losing effects")
			}
			t.Logf("observed native %s writer backend %d and competing waiter", kind, writer)
		})
	}
}
