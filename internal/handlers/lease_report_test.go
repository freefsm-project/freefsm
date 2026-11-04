package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseReportHistoricalPositionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Report customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Report premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Report equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Report asset").SaveX(ctx)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	lease, err := svc.RegisterDraftLease(ctx, actor, customer.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, 0, -10).Format(time.DateOnly)
	end := today.AddDate(0, 0, 10).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Historical report agreement", StartsOn: start, EndsOn: end, PremisesID: &location.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(12345)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: today.Format(time.DateOnly)}))
	body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+start, 200)
	for _, want := range []string{"Historical report agreement", "active", "Updated at", "Report asset", "123.45", "1 matching Leases"} {
		assertContains(t, body, want)
	}
	assertContains(t, body, fmt.Sprintf(`href="#lease-report-%d"`, lease.ID))
	assertContains(t, body, fmt.Sprintf(`id="lease-report-%d"`, lease.ID))
	assertContains(t, body, "Current Lease workspace")
	body = requestBody(t, web, cookie, "GET", "/leases/report?as_of="+today.AddDate(0, 0, -11).Format(time.DateOnly), 200)
	assertContains(t, body, "scheduled")
	assertContains(t, body, "Scheduled monthly rent: 123.45")
	assertNotContains(t, body, "Report asset")
	role, err := svc.SaveRole(ctx, actor, 0, "Report base", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Report reader").SetEmail("report74@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
	reader := sessionCookie(t, ctx, sessions, user.ID)
	body = requestBody(t, web, reader, "GET", "/leases/report?as_of="+start, 200)
	assertContains(t, body, "Historical report agreement")
	for _, hidden := range []string{"123.45", "Report asset", "Report customer", "Report premises"} {
		assertNotContains(t, body, hidden)
	}
	requestBody(t, web, reader, "GET", "/leases/report?billing=review", 403)
	requestBody(t, web, reader, "GET", fmt.Sprintf("/leases/report?asset=%d", asset.ID), 403)
	requestBody(t, web, cookie, "GET", "/leases/report?as_of=bad", 422)
	requestBody(t, web, cookie, "GET", "/leases/report?as_of="+url.QueryEscape(today.AddDate(0, 0, 1).Format(time.DateOnly)), 422)
	t.Run("placement is independent effective history", func(t *testing.T) {
		historyCheck(t, svc.ChangeAssetAssociation(ctx, actor, asset.ID, authorization.AssetAssociationInput{Action: "place", EffectiveDate: today.AddDate(0, 0, -5).Format(time.DateOnly), Reason: "Recorded placement", CustomerID: customer.ID, LocationID: location.ID}))
		body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+start+"&placement=unplaced", 200)
		assertContains(t, body, "1 matching Leases")
		assertContains(t, body, "Report asset")
		assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?placement=unplaced", 200), "0 matching Leases")
		historyCheck(t, svc.ChangeAssetAssociation(ctx, actor, asset.ID, authorization.AssetAssociationInput{Action: "return", EffectiveDate: today.Format(time.DateOnly), Reason: "Returned without ending coverage", Revision: 1}))
		body = requestBody(t, web, cookie, "GET", "/leases/report?placement=unplaced", 200)
		assertContains(t, body, "1 matching Leases")
		assertContains(t, body, "Report asset")
		assertContains(t, body, "Monthly rent: 123.45")
	})
	t.Run("stable filtered pages and totals", func(t *testing.T) {
		for range 26 {
			_, err := svc.RegisterDraftLease(ctx, actor, customer.ID)
			historyCheck(t, err)
		}
		body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/report?customer=%d&per_page=25", customer.ID), 200)
		assertContains(t, body, "27 matching Leases")
		assertContains(t, body, "Matching monthly rent: 123.45")
		assertContains(t, body, ">Next</a>")
		second := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/report?customer=%d&per_page=25&page=2", customer.ID), 200)
		assertContains(t, second, "27 matching Leases")
		assertContains(t, second, "Matching monthly rent: 123.45")
		assertNotContains(t, second, "Historical report agreement")
		assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?lifecycle=draft&as_of="+start, 200), "0 matching Leases")
	})
	t.Run("future coverage and billing do not leak", func(t *testing.T) {
		future := today.AddDate(0, 0, 1).Format(time.DateOnly)
		incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Future report Asset secret").SaveX(ctx)
		extra := int64(7000)
		_, err := svc.ChangeLeaseCoverage(ctx, actor, lease.ID, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: future, IncomingID: incoming.ID}, Action: "create", Reason: "Future coverage", OperationKey: newOperationKey(), RentCents: &extra, TaxRate: "0"})
		historyCheck(t, err)
		historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease.ID, authorization.LeaseBillingRevisionInput{EffectiveOn: future, Reason: "Future rent", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: asset.ID, RentCents: 99999, TaxRate: "0"}}}))
		body := requestBody(t, web, cookie, "GET", "/leases/report?search=Historical", 200)
		assertContains(t, body, "Monthly rent: 123.45")
		assertContains(t, body, "Approved rent schedule")
		assertContains(t, body, "999.99")
		assertContains(t, body, "Matching monthly rent: 123.45")
		assertNotContains(t, body, "Future report Asset secret")
		assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/report?asset=%d", incoming.ID), 200), "0 matching Leases")
	})
	t.Run("explicit refresh failure", func(t *testing.T) {
		_, err := pool.Exec(ctx, `ALTER TABLE lease_billing_revisions RENAME TO unavailable_report_revisions`)
		historyCheck(t, err)
		defer func() {
			_, err := pool.Exec(ctx, `ALTER TABLE unavailable_report_revisions RENAME TO lease_billing_revisions`)
			historyCheck(t, err)
		}()
		body := requestBody(t, web, cookie, "GET", "/leases/report?search=Historical&as_of="+start, 503)
		assertContains(t, body, "report could not be refreshed")
		assertContains(t, body, "search=Historical")
		assertNotContains(t, body, "0 matching Leases")
	})
	t.Run("responsive persisted report", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-report.cjs", server.URL, cookie.Value, start).CombinedOutput()
		t.Logf("report browser: %s", output)
		historyCheck(t, err)
	})
	t.Run("queued refresh rechecks grants", func(t *testing.T) {
		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		tx, err := pool.Begin(waitCtx)
		historyCheck(t, err)
		defer tx.Rollback(ctx)
		_, err = tx.Exec(waitCtx, `SELECT pg_advisory_xact_lock(190019)`)
		historyCheck(t, err)
		_, err = tx.Exec(waitCtx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.view'`, role)
		historyCheck(t, err)
		var pid int
		historyCheck(t, tx.QueryRow(waitCtx, `SELECT pg_backend_pid()`).Scan(&pid))
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("GET", "/leases/report", nil).WithContext(waitCtx)
			r.AddCookie(reader)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			done <- w
		}()
		for {
			var waiting bool
			historyCheck(t, pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting))
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		historyCheck(t, tx.Commit(waitCtx))
		response := <-done
		if response.Code != 403 || strings.Contains(response.Body.String(), "Historical report agreement") {
			t.Fatalf("stale authority response: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestLeaseReportRecordedInvoiceHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, finalization := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	body := requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertContains(t, body, "Source Asset: Covered equipment")
	path := fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", lease, invoice)
	review := requestBody(t, web, cookie, "GET", path, 200)
	fieldPost(t, web, cookie, path, url.Values{"invoice_id": {fmt.Sprint(invoice)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"abatement"}, "amount_cents": {"1"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-02"}, "reason": {"Report retained allowance"}, "operation_key": {jobExecutionHidden(t, review, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, review, "invoice_revision")}}, 303)
	body = requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertContains(t, body, "Report retained allowance")
	assertContains(t, body, "abatement")
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Report billing only", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.billing.view", Scope: authorization.All}})
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Report billing").SetEmail("report-billing74@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role))
	reader := sessionCookie(t, ctx, sessions, user.ID)
	body = requestBody(t, web, reader, "GET", "/leases/report", 200)
	assertNotContains(t, body, "Recorded generation")
	assertNotContains(t, body, "Report retained allowance")
	assertContains(t, body, "Generation status unavailable under current Invoice permissions")
	requestBody(t, web, reader, "GET", "/leases/report?billing=review", 403)
	voidLeaseInvoiceAndReplay(t, pool, web, cookie, company, admin, lease, finalization)
	body = requestBody(t, web, cookie, "GET", "/leases/report?billing=void", 200)
	assertContains(t, body, "1 matching Leases")
	assertContains(t, body, "invoice:void")
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, company).Scan(&today))
	historyCheck(t, svc.ChangeLeaseNotice(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "company", NoticeOn: today, EffectiveOn: today, Reason: "Report cancellation", OperationKey: newOperationKey()}))
	body = requestBody(t, web, cookie, "GET", "/leases/report?billing=review", 200)
	assertContains(t, body, "1 matching Leases")
	assertContains(t, body, "Generated-Invoice exception")
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/delete", lease), nil, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?archive=archived", 200), "1 matching Leases")
	assertContains(t, requestBody(t, web, reader, "GET", "/leases/report", 200), "0 matching Leases")
	requestBody(t, web, reader, "GET", "/leases/report?archive=all", 403)
}

func TestLeaseReportGenerationKnowledgeAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, finalization := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	var due string
	historyCheck(t, pool.QueryRow(ctx, `SELECT initial_due_on::text FROM lease_finalizations WHERE lease_id=$1`, lease).Scan(&due))
	// Advance the fixture company clock to the actual managed due date, so
	// generation-dependent period membership is exercised, not a future period.
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, due))
	historyCheck(t, err)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.billing.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}}
	role, err := svc.SaveRole(ctx, actor, 0, "Partial report generation visibility", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Partial report reader").SetEmail("report-partial74@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
	reader := sessionCookie(t, ctx, sessions, user.ID)
	checkUnknown := func() {
		t.Helper()
		body := requestBody(t, web, reader, "GET", "/leases/report", 200)
		assertContains(t, body, "Generation status unavailable under current Invoice permissions")
		assertNotContains(t, body, "Recorded generation:")
		assertNotContains(t, body, ": ungenerated")
		assertNotContains(t, body, `value="ungenerated"`)
		invalid := requestBody(t, web, reader, "GET", "/leases/report?billing=ungenerated&search=retained", 422)
		assertContains(t, invalid, "Ungenerated periods require All Records Invoice and Pricing access, including archived Invoices. Choose another Billing exception or clear filters.")
		assertContains(t, invalid, `value="retained"`)
		assertNotContains(t, invalid, "matching Leases")
		assertContains(t, invalid, "Clear filters")
		requestBody(t, web, reader, "GET", "/leases/report?billing=review", 200)
		requestBody(t, web, reader, "GET", "/leases/report?billing=void", 200)
	}
	checkUnknown() // The generated Invoice is not assigned to this reader.
	voidLeaseInvoiceAndReplay(t, pool, web, cookie, company, admin, lease, finalization)
	fieldPost(t, web, cookie, fmt.Sprintf("/invoices/%d/delete", invoice), nil, 303)
	grants[2].Scope, grants[3].Scope = authorization.All, authorization.All
	_, err = svc.SaveRole(ctx, actor, role, "Partial report generation visibility", grants, 1)
	historyCheck(t, err)
	checkUnknown() // All Records without archived visibility still cannot know absence.
	// A later managed month is genuinely due without running the worker.
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT ('%s'::date + interval '2 months')::date $$`, due))
	historyCheck(t, err)
	grants = append(grants, authorization.Grant{Permission: "invoice.archived.view", Scope: authorization.All})
	_, err = svc.SaveRole(ctx, actor, role, "Partial report generation visibility", grants, 2)
	historyCheck(t, err)
	body := requestBody(t, web, reader, "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertNotContains(t, body, "Generation status unavailable")
	assertContains(t, body, `value="ungenerated"`)
	assertContains(t, requestBody(t, web, reader, "GET", "/leases/report?billing=void", 200), "1 matching Leases")
	// Save a successful full-authority negative filter before revoking archive
	// visibility. The same persisted preference must recover on the next entry.
	r := httptest.NewRequest("GET", "/leases/report?billing=ungenerated", nil)
	r.AddCookie(reader)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("complete generation filter: %d", w.Code)
	}
	assertContains(t, w.Body.String(), "1 matching Leases")
	var saved *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Path == "/leases/report" {
			saved = c
		}
	}
	if saved == nil {
		t.Fatal("missing remembered generation filter")
	}
	_, err = svc.SaveRole(ctx, actor, role, "Partial report generation visibility", grants[:4], 3)
	historyCheck(t, err)
	checkUnknown() // Grant loss is effective on the very next report request.
	r = httptest.NewRequest("GET", "/leases/report", nil)
	r.AddCookie(reader)
	r.AddCookie(saved)
	w = httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/leases/report?reset=1" {
		t.Fatalf("remembered generation recovery: %d %s", w.Code, w.Body.String())
	}
	if cookies := w.Result().Cookies(); len(cookies) == 0 || cookies[0].MaxAge != -1 {
		t.Fatal("incompatible remembered filter was not cleared")
	}
	t.Run("generation filter browser recovery", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-report.cjs", server.URL, reader.Value, "generation", saved.Name, saved.Value, cookie.Value).CombinedOutput()
		t.Logf("generation filter browser: %s", output)
		historyCheck(t, err)
	})
	grants[2].Scope, grants[3].Scope = authorization.Assigned, authorization.Assigned
	_, err = svc.SaveRole(ctx, actor, role, "Partial report generation visibility", grants[:4], 4)
	historyCheck(t, err)
	readerActor := authorization.Actor{UserID: user.ID}
	before, err := svc.LeaseReport(ctx, readerActor, authorization.LeaseReportQuery{})
	historyCheck(t, err)
	var generatedBefore, generatedAfter int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&generatedBefore))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&generatedAfter))
	if generatedAfter <= generatedBefore {
		t.Fatal("comparison did not create a previously absent hidden generation")
	}
	after, err := svc.LeaseReport(ctx, readerActor, authorization.LeaseReportQuery{})
	historyCheck(t, err)
	before.UpdatedAt, after.UpdatedAt = "", ""
	if !reflect.DeepEqual(before, after) {
		t.Fatal("hidden generation changed authorized report rows, totals, membership or periods")
	}
	checkUnknown()
}
