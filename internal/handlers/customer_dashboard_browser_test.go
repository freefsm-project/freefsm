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
	"github.com/justinas/nosurf"
)

func TestCustomerDashboardPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser operational Customer").SaveX(ctx)
	client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Persisted Contact").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Persisted direct Job").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetNumber("CURRENT-BROWSER-104").SetName("Machine").SetAssetTypeID(typ.ID).SaveX(ctx)
	past := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("PAST-BROWSER-104").SetName("Former machine").SetAssetTypeID(typ.ID).SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE NEXT CUSTOMER").SaveX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/transfer", past.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "location_id": {"0"}, "effective_date": {"2020-01-02"}, "reason": {"Transferred"}, "revision": {"0"}}, 303)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser dashboard reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Browser limited reader").SetEmail("browser-dashboard@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	limited := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	// Test-only external changes/faults, served outside production routing. Every
	// observation and navigation under test still uses the production handlers.
	mux.HandleFunc("POST /__fixture/{change}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.PathValue("change") {
		case "contacts-off":
			_, err = pool.Exec(ctx, `ALTER TABLE customer_contacts RENAME TO dashboard_browser_contacts`)
		case "contacts-on":
			_, err = pool.Exec(ctx, `ALTER TABLE dashboard_browser_contacts RENAME TO customer_contacts`)
		case "unassign":
			_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
		case "archive-job":
			_, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, j.ID)
		case "revoke":
			_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, role)
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
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/customer-dashboard.cjs", server.URL, cookie.Value, limited.Value, fmt.Sprint(c.ID), fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
