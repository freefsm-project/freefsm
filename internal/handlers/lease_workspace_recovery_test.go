package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseWorkspaceRememberedAuthorityBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Recovery premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Recovery equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Recovery asset").SaveX(ctx)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, 0, 5).Format(time.DateOnly)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Recovery visible agreement", StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	_, err = pool.Exec(ctx, `INSERT INTO lease_billing_period_failures(lease_id,kind,due_on,failure) VALUES($1,'monthly',CURRENT_DATE,'restricted recovery failure detail')`, lease.ID)
	historyCheck(t, err)
	archived, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, archived.ID, authorization.DraftLeaseDetailsInput{Title: "Restricted archived recovery agreement"}))
	historyCheck(t, svc.LeaseLifecycle(ctx, actor, archived.ID, false))
	for _, scenario := range []struct{ name, permission, query string }{
		{"billing", "lease.billing.view", "queue=failed"},
		{"archived", "lease.archived.view", "state=archived"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: scenario.permission, Scope: authorization.All}}
			role, err := svc.SaveRole(ctx, actor, 0, "Recovery "+scenario.name, grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetName("Recovery reader").SetEmail(scenario.name + "@recovery73.test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, actor, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			mux := http.NewServeMux()
			mux.HandleFunc("/fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
				_, err := svc.SaveRole(r.Context(), actor, role, "Recovery "+scenario.name, grants[:1], 1)
				if err != nil {
					http.Error(w, "fixture failed", 500)
					return
				}
				w.WriteHeader(204)
			})
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
			mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
			server := httptest.NewServer(mux)
			defer server.Close()
			browserCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-workspace-recovery.cjs", server.URL, cookie.Value, scenario.query).CombinedOutput()
			t.Logf("recovery browser: %s", output)
			historyCheck(t, err)
		})
	}
}

func TestLeaseWorkspaceEmptyStatesBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Empty workspace customer").SaveX(ctx)
	role, err := svc.SaveRole(ctx, actor, 0, "Empty workspace reader", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Empty reader").SetEmail("empty73@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, u.ID, role))
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/create", func(w http.ResponseWriter, r *http.Request) {
		lease, err := svc.RegisterDraftLease(r.Context(), actor, c.ID)
		if err == nil && r.URL.Query().Get("archived") == "1" {
			err = svc.LeaseLifecycle(r.Context(), actor, lease.ID, false)
		}
		if err != nil {
			http.Error(w, "fixture failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-workspace-empty.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, u.ID).Value).CombinedOutput()
	t.Logf("empty browser: %s", output)
	historyCheck(t, err)
}
