package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type billingNavigationFixture struct {
	pool                            *pgxpool.Pool
	web                             http.Handler
	viewer, baseOnly, foreign, none *http.Cookie
	lease, otherLease, role, admin  int64
	ids                             []int64
}

func billingNavigationSetup(t *testing.T) billingNavigationFixture {
	t.Helper()
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	a := authorization.Actor{UserID: admin}
	svc := authorization.New(pool)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '1999-12-02'::date $$`)
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Navigation Customer").SaveX(ctx)
	premises := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Navigation premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Navigation equipment").SaveX(ctx)
	var assets []int64
	var assetValues []string
	for i := 0; i < 3; i++ {
		asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName(fmt.Sprintf("Navigation Asset %d", i)).SaveX(ctx)
		assets = append(assets, asset.ID)
		assetValues = append(assetValues, fmt.Sprint(asset.ID))
	}
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(customer.ID)}}, 303)
	var lease int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&lease))
	path := fmt.Sprintf("/leases/%d", lease)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2000-01-01"}, "ends_on": {"2000-12-31"}, "premises_id": {fmt.Sprint(premises.ID)}, "asset_ids": assetValues}, 303)
	initial, rent := int64(101), int64(5000)
	for i, asset := range assets {
		historyCheck(t, svc.SaveDraftLeaseBilling(ctx, a, lease, int64(i), "2000-01-01", authorization.DraftLeaseTerms{AssetID: asset, InitialCents: &initial, RentCents: &rent, TaxRate: "5"}, 0))
	}
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"3"}, "managed_billing_start": {"2000-01-01"}}, 303)
	oldEnd, newEnd := "2000-01-31", "2010-05-31"
	coverage := []authorization.LeaseAssetDependency{{AssetID: assets[0], StartsOn: "2000-01-01"}, {AssetID: assets[1], StartsOn: "2000-01-01", EndsOn: &oldEnd}, {AssetID: assets[2], StartsOn: "2000-01-01", EndsOn: &newEnd}}
	writeCoverage := func(rows []authorization.LeaseAssetDependency) {
		t.Helper()
		historyCheck(t, svc.WithinActor(ctx, a, func(tx pgx.Tx, _ authorization.Principal) error {
			return svc.WriteLeaseDependencies(ctx, tx, a, authorization.LeaseDependencies{LeaseID: lease, CustomerID: customer.ID, PremisesID: &premises.ID, Lifecycle: "scheduled", Coverage: rows})
		}))
	}
	writeCoverage(coverage)
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"PRIVATE_BASE_COLLAB_NAVIGATION"}}, 200)
	// All 126 period identities and generated Invoices below come from production
	// finalization/scheduling. SQL is explicitly a storage-fault/clock fixture:
	// two generation failures and one pre-materialization failure remain visible.
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2010-05-02'::date $$;
 CREATE FUNCTION navigation_invoice_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' AND EXISTS(SELECT 1 FROM lease_billing_obligations WHERE id=(NEW.detail->>'obligation_id')::bigint AND due_on IN('2000-02-01','2010-06-01')) THEN RAISE EXCEPTION 'navigation generation fault'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER navigation_invoice_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION navigation_invoice_fault();
 CREATE FUNCTION navigation_obligation_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.due_on='2000-03-01' THEN RAISE EXCEPTION 'navigation materialization fault'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER navigation_obligation_fault BEFORE INSERT ON lease_billing_obligations FOR EACH ROW EXECUTE FUNCTION navigation_obligation_fault()`)
	historyCheck(t, err)
	for pass := 0; pass < 6; pass++ {
		if err := svc.GenerateLeaseDrafts(ctx); err == nil {
			t.Fatal("fixture storage faults were hidden")
		}
	}
	// Actual owning coverage corrections flag just the initial January 2000
	// generation and May 2010 generation. No review rows or Invoices are seeded.
	frozen := leasePreservationRows(t, pool, []string{"invoices", "lease_invoice_generations"})
	newEnd = "2010-04-30"
	writeCoverage([]authorization.LeaseAssetDependency{coverage[0], coverage[2]})
	historyEqual(t, "real coverage correction preserves generated bytes", leasePreservationRows(t, pool, []string{"invoices", "lease_invoice_generations"}), frozen)
	other, err := svc.RegisterDraftLease(ctx, a, customer.ID)
	historyCheck(t, err)
	f := billingNavigationFixture{pool: pool, web: web, lease: lease, otherLease: other.ID, admin: admin}
	for i, permissions := range [][]string{{"lease.billing.view"}, {"lease.view"}, {}} {
		var grants []authorization.Grant
		for _, key := range permissions {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, a, 0, fmt.Sprintf("Navigation viewer %d", i), grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName("Navigation viewer").SetEmail(fmt.Sprintf("navigation%d@test", i)).SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, a, u.ID, role))
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		if i == 0 {
			f.viewer = cookie
			f.role = role
		} else if i == 1 {
			f.baseOnly = cookie
		} else {
			f.none = cookie
		}
	}
	var otherCompany int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other navigation company','other-navigation') RETURNING id`).Scan(&otherCompany))
	foreign := client.User.Create().SetCompanyID(otherCompany).SetName("Foreign viewer").SetEmail("navigation-foreign@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, a, foreign.ID, f.role))
	f.foreign = sessionCookie(t, ctx, sessions, foreign.ID)
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY due_on DESC,id DESC) FROM lease_billing_obligations WHERE lease_id=$1`, lease).Scan(&f.ids))
	historyEqual(t, "real materialized period fixture", len(f.ids), 125)
	return f
}

func TestLeaseBillingHistoryPaginationBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	f := billingNavigationSetup(t)
	tables := []string{"lease_finalizations", "lease_billing_obligations", "lease_billing_obligation_revisions", "lease_invoice_generations", "lease_invoice_review_flags", "lease_billing_source_changes", "lease_billing_attempts", "lease_billing_period_failures", "lease_billing_retry_receipts", "lease_billing_period_dispositions", "lease_asset_dependencies", "invoices", "issued_document_numbers", "company_settings", "comments"}
	before := leasePreservationRows(t, f.pool, tables)
	mux := http.NewServeMux()
	// A test-only control performs a real reviewed Role edit after navigation.
	mux.HandleFunc("/test/revoke-billing", func(w http.ResponseWriter, r *http.Request) {
		var revision int64
		err := f.pool.QueryRow(r.Context(), `SELECT revision FROM authorization_roles WHERE id=$1`, f.role).Scan(&revision)
		if err == nil {
			_, err = authorization.New(f.pool).SaveRole(r.Context(), authorization.Actor{UserID: f.admin}, f.role, "Revoked browser billing viewer", nil, revision)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(f.web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ids, err := json.Marshal(f.ids)
	historyCheck(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/lease-billing-pagination.cjs", server.URL, f.viewer.Value, f.baseOnly.Value, f.none.Value, f.foreign.Value, fmt.Sprint(f.lease), string(ids)).CombinedOutput()
	t.Logf("Chromium billing history: %s", output)
	historyCheck(t, err)
	historyEqual(t, "persisted browser navigation preserves billing and collaboration history", leasePreservationRows(t, f.pool, tables), before)
}

func TestLeaseBillingHistoryPaginationHTTP(t *testing.T) {
	f := billingNavigationSetup(t)
	path := fmt.Sprintf("/leases/%d/billing", f.lease)
	body := requestBody(t, f.web, f.viewer, "GET", path, 200)
	assertContains(t, body, "126 billing periods")
	links := regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(body, -1)
	historyEqual(t, "bounded first page links", len(links), 25)
	assertContains(t, body, "Next")
	ctx := context.Background()
	tables := []string{"lease_finalizations", "lease_billing_obligations", "lease_billing_obligation_revisions", "lease_invoice_generations", "lease_invoice_review_flags", "lease_billing_source_changes", "lease_billing_attempts", "lease_billing_period_failures", "lease_billing_retry_receipts", "lease_billing_period_dispositions", "lease_asset_dependencies", "invoices", "issued_document_numbers", "authorization_audit", "company_settings"}
	before := leasePreservationRows(t, f.pool, tables)
	var seen []int64
	for page := 1; page <= 6; page++ {
		body := requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("%s?page=%d&per_page=25&filter=all", path, page), 200)
		assertContains(t, body, fmt.Sprintf("Page %d of 6", page))
		for _, match := range regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(body, -1) {
			id, err := strconv.ParseInt(match[1], 10, 64)
			historyCheck(t, err)
			seen = append(seen, id)
		}
		if page == 1 && (strings.Contains(body, "initial due 2000-01-01") || strings.Contains(body, "monthly due 2000-02-01")) {
			t.Fatal("off-page period leaked into first page")
		}
		if page == 6 {
			assertContains(t, body, "initial due 2000-01-01")
			assertContains(t, body, "First")
			if strings.Contains(body, ">Next</a>") {
				t.Fatal("last page offers a nonexistent next page")
			}
		}
	}
	historyEqual(t, "all materialized period links once in stable due-date/ID order", seen, f.ids)
	review := requestBody(t, f.web, f.viewer, "GET", path+"?filter=review&per_page=50", 200)
	assertContains(t, review, "2 billing periods")
	assertContains(t, review, "initial due 2000-01-01")
	assertContains(t, review, "monthly due 2010-05-01")
	if strings.Contains(review, "Coverage changed; review this generated period") {
		t.Fatal("list unnecessarily loaded detailed review reasons")
	}
	old := f.ids[len(f.ids)-1]
	detail := requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("%s/periods/%d?filter=review&page=1&per_page=50", path, old), 200)
	assertContains(t, detail, "Coverage changed; review this generated period")
	assertContains(t, detail, "filter=review&amp;page=1&amp;per_page=50")
	if strings.Contains(detail, `href="/invoices/`) {
		t.Fatal("Billing-only viewer gained an Invoice link")
	}
	failed := requestBody(t, f.web, f.viewer, "GET", path+"?filter=failed", 200)
	assertContains(t, failed, "3 billing periods")
	for _, date := range []string{"2000-02-01", "2000-03-01", "2010-06-01"} {
		assertContains(t, failed, "monthly due "+date)
	}
	assertContains(t, failed, "No obligation materialized.")
	historyEqual(t, "unmaterialized failure has no invented link", len(regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(failed, -1)), 2)
	for _, query := range []string{"?page=bad&per_page=bad&filter=unknown", "?page=-1&per_page=26", "?page=0&per_page=0"} {
		body := requestBody(t, f.web, f.viewer, "GET", path+query, 200)
		assertContains(t, body, "Page 1 of 6")
		historyEqual(t, "invalid size uses standard 25", len(regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(body, -1)), 25)
	}
	requestBody(t, f.web, f.viewer, "GET", path+"?page=1000001", 409)
	requestBody(t, f.web, f.viewer, "GET", path+"?page=999999999999999999999999999", 409)
	empty := requestBody(t, f.web, f.viewer, "GET", path+"?page=7", 200)
	historyEqual(t, "past last page has no duplicate links", len(regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(empty, -1)), 0)
	assertContains(t, empty, ">Last</a>")
	for _, cookie := range []*http.Cookie{f.baseOnly, f.none, f.foreign} {
		for _, suffix := range []string{"?filter=review", "?filter=failed&page=2", fmt.Sprintf("/periods/%d?filter=review", old)} {
			denied := requestBody(t, f.web, cookie, "GET", path+suffix, 404)
			for _, secret := range []string{"126 billing periods", "Coverage changed", "5000", "2000-01-01"} {
				if strings.Contains(denied, secret) {
					t.Fatalf("denial disclosed %q", secret)
				}
			}
		}
	}
	requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("/leases/%d/billing/periods/%d", f.otherLease, old), 404)
	var invoice int64
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT invoice_id FROM lease_invoice_generations WHERE obligation_id=$1`, old).Scan(&invoice))
	requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("/invoices/%d", invoice), 403)
	requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("/leases/%d/comments", f.lease), 404)
	historyEqual(t, "all navigation GETs preserve historical facts", leasePreservationRows(t, f.pool, tables), before)
	t.Run("multiple flags and filtered last page", func(t *testing.T) {
		end := "2010-05-01"
		svc := authorization.New(f.pool)
		actor := authorization.Actor{UserID: f.admin}
		historyCheck(t, svc.WithinActor(ctx, actor, func(tx pgx.Tx, _ authorization.Principal) error {
			return svc.RecordLeaseBillingSourceChange(ctx, tx, actor, f.lease, "2000-01-01", &end, "Declared wider navigation review request")
		}))
		// Existing old/new flags now have another legitimate review record. The
		// list must count periods, not multiply their rows by flag history.
		var expected []int64
		historyCheck(t, f.pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY due_on DESC,id DESC) FROM lease_billing_obligations WHERE lease_id=$1 AND due_on NOT IN('2000-02-01','2010-06-01')`, f.lease).Scan(&expected))
		historyEqual(t, "generated fixture periods", len(expected), 123)
		frozen := leasePreservationRows(t, f.pool, tables)
		var links []int64
		for page := 1; page <= 2; page++ {
			body := requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("%s?filter=review&per_page=100&page=%d", path, page), 200)
			assertContains(t, body, "123 billing periods")
			for _, match := range regexp.MustCompile(`/billing/periods/([0-9]+)`).FindAllStringSubmatch(body, -1) {
				id, err := strconv.ParseInt(match[1], 10, 64)
				historyCheck(t, err)
				links = append(links, id)
			}
			if page == 1 {
				assertContains(t, body, "filter=review&amp;page=2&amp;per_page=100")
			} else {
				assertContains(t, body, "initial due 2000-01-01")
			}
		}
		historyEqual(t, "filtered pages neither omit nor duplicate multiply-flagged periods", links, expected)
		historyEqual(t, "filtered GETs preserve flags and historical facts", leasePreservationRows(t, f.pool, tables), frozen)
	})
	var revision int64
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, f.role).Scan(&revision))
	_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, f.role, "Revoked navigation viewer", nil, revision)
	historyCheck(t, err)
	requestBody(t, f.web, f.viewer, "GET", path+"?page=6&filter=all", 404)
	requestBody(t, f.web, f.viewer, "GET", fmt.Sprintf("%s/periods/%d?filter=review", path, old), 404)
}
