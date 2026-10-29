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
	for _, kind := range []string{"finalization", "asset", "location", "customer", "placement", "asset-first", "placement-first"} {
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
			}
			command := creditCommand{path: fmt.Sprintf("/leases/%d/finalize", id), values: values}
			action := "lease.finalize"
			reverse := kind == "asset-first" || kind == "placement-first"
			if reverse {
				command, competitor = competitor, command
				action = "asset.archive"
				if kind == "placement-first" {
					action = "asset.place"
				}
			}
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION pause_finalization() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN PERFORM pg_advisory_xact_lock(660201); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_finalization BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_finalization()`, action))
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
			if r := <-second; r.Code != 409 {
				t.Fatalf("competitor: %d %s", r.Code, r.Body.String())
			}
			var valid bool
			if reverse {
				historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_finalizations) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations) AND NOT EXISTS(SELECT 1 FROM invoices) AND NOT EXISTS(SELECT 1 FROM issued_document_numbers WHERE document_type='invoice') AND EXISTS(SELECT 1 FROM leases WHERE id=$1 AND lifecycle='draft' AND details_revision=1)`, id).Scan(&valid))
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
