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
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestCustomerDashboardPersistedServiceBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser service Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE NEXT CUSTOMER").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("BROWSER-SERVICE-104").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	past := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("PAST-SERVICE-104").SetName("Former pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:completed")).FirstX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetStatusID(completed.ID).SetJobType("Browser service Job").SetNotes("PRIVATE JOB FACET").SaveX(ctx)
	oldJob := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(past.ID).SetJobType("Original Customer Job").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	today := time.Now().UTC().Format("2006-01-02")
	create := func(job, asset int64, summary string) {
		t.Helper()
		serviceEntryPost(t, router, adminCookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", job, asset), url.Values{"service_date": {today}, "summary": {summary}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	}
	create(j.ID, a.ID, "Browser dashboard service")
	create(oldJob.ID, past.ID, "Original Customer service")
	fieldPost(t, router, adminCookie, fmt.Sprintf("/assets/%d/associations/transfer", past.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "effective_date": {today}, "reason": {"Sale"}, "revision": {"0"}}, 303)
	nextJob := client.Job.Create().SetCompanyID(company).SetCustomerID(next.ID).SetAssetID(past.ID).SetJobType("PRIVATE NEXT JOB").SaveX(ctx)
	create(nextJob.ID, past.ID, "PRIVATE NEXT SERVICE")
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}, {Permission: "service_entry.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Browser independent service reader", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Browser history reader").SetEmail("browser-history104@test").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	if err = auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	// External fixture changes never replace production read or revision paths.
	mux.HandleFunc("POST /__fixture/{change}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.PathValue("change") {
		case "service-off":
			_, err = pool.Exec(ctx, `ALTER TABLE asset_service_revisions RENAME TO dashboard_browser_service_off`)
		case "service-on":
			_, err = pool.Exec(ctx, `ALTER TABLE dashboard_browser_service_off RENAME TO asset_service_revisions`)
		case "archive":
			_, err = pool.Exec(ctx, `UPDATE assets SET deleted_at=now() WHERE id=$1`, a.ID)
		case "revoke":
			_, err = auth.SaveRole(ctx, actor, role, "Browser independent service reader", grants[:2], 1)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	bounded, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	out, err := exec.CommandContext(bounded, "node", "testdata/browser/customer-dashboard-service.cjs", server.URL, cookie.Value, adminCookie.Value, fmt.Sprint(c.ID), fmt.Sprint(j.ID), fmt.Sprint(a.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
