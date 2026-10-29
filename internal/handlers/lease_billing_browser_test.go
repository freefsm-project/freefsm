package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

func TestLeaseBillingRetryReplacementBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser billing").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Covered").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var lease int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&lease))
	path := fmt.Sprintf("/leases/%d", lease)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-12-31"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}}, 303)
	amount := int64(4321)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, lease, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 303)
	var obligation int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE kind='monthly'`).Scan(&obligation))
	cookies := []string{}
	for i, keys := range [][]string{{"lease.view", "lease.billing.view", "lease.billing.manage", "lease.manage"}, {"lease.view", "lease.manage"}, {"lease.billing.view"}} {
		grants := []authorization.Grant{}
		for _, key := range keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("Browser billing %d", i), grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName("Reviewer").SetEmail(fmt.Sprintf("billing67-%d@test", i)).SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		cookies = append(cookies, sessionCookie(t, ctx, sessions, u.ID).Value)
	}
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$; CREATE FUNCTION browser_billing_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' THEN RAISE EXCEPTION 'browser generation fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER browser_billing_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION browser_billing_fault()`)
	historyCheck(t, err)
	if err = svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("missing injected failure")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/test/repair", func(w http.ResponseWriter, r *http.Request) {
		_, err := pool.Exec(r.Context(), `DROP TRIGGER browser_billing_fault ON authorization_audit`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/test/void", func(w http.ResponseWriter, r *http.Request) {
		var invoice, target int64
		err := pool.QueryRow(r.Context(), `SELECT invoice_id FROM lease_invoice_generations WHERE obligation_id=$1`, obligation).Scan(&invoice)
		if err == nil {
			err = pool.QueryRow(r.Context(), `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&target)
		}
		flow := statusflow.New(pool)
		actor := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
		if err == nil {
			err = flow.FinalizeInvoice(r.Context(), actor, invoice)
		}
		if err == nil {
			err = flow.TransitionInvoice(r.Context(), actor, invoice, target)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/test/coverage", func(w http.ResponseWriter, r *http.Request) {
		var before, after string
		err := pool.QueryRow(r.Context(), `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM invoices i`).Scan(&before)
		end := "2024-01-31"
		if err == nil {
			err = svc.WithinActor(r.Context(), authorization.Actor{UserID: admin}, func(tx pgx.Tx, _ authorization.Principal) error {
				return svc.WriteLeaseDependencies(r.Context(), tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: lease, CustomerID: c.ID, PremisesID: &l.ID, Lifecycle: "active", Coverage: []authorization.LeaseAssetDependency{{AssetID: asset.ID, StartsOn: "2024-01-01", EndsOn: &end}}})
			})
		}
		if err == nil {
			err = pool.QueryRow(r.Context(), `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM invoices i`).Scan(&after)
		}
		if err != nil || before != after {
			http.Error(w, "coverage changed Invoice bytes or failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-billing.cjs", server.URL, cookies[0], cookies[1], fmt.Sprint(lease), fmt.Sprint(obligation), cookies[2]).CombinedOutput()
	t.Logf("Chromium billing: %s", output)
	historyCheck(t, err)
	var generations, receipts int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations WHERE obligation_id=$1`, obligation).Scan(&generations))
	historyEqual(t, "one root and one replacement", generations, 2)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_retry_receipts WHERE obligation_id=$1`, obligation).Scan(&receipts))
	historyEqual(t, "single retry receipt", receipts, 1)
}
