package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestEstimateCohortConvertRevertReconvertHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cohort Customer").SaveX(ctx)
	// Deliberately earlier than the generated Invoices: Invoice creation must
	// never determine inclusion in the source Estimate creation cohort.
	created := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Original cohort identity").SetCreatedAt(created).SaveX(ctx)
	cohort := "/reports/estimate-cohort?scope=A&created_from=2024-03-10&created_to=2024-03-10"
	assertContains(t, requestBody(t, web, cookie, "GET", cohort, 200), e.Title)
	var invoices []int64
	for cycle := 0; cycle < 2; cycle++ {
		path := fmt.Sprintf("/estimates/%d/convert", e.ID)
		fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
		var invoice int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, e.ID).Scan(&invoice))
		invoices = append(invoices, invoice)
		body := requestBody(t, web, cookie, "GET", cohort+"&conversion=active", 200)
		workReportTotal(t, body, "Distinct Estimates", 1)
		workReportTotal(t, body, "Active conversions (separate from category)", 1)
		assertContains(t, body, e.Title)
		assertContains(t, body, "Active conversion:")
		assertContains(t, body, fmt.Sprintf(`href="/invoices/%d"`, invoice))
		assertNotContains(t, body, fmt.Sprintf(`href="/estimates/%d"`, e.ID))
		expectStatus(t, web, cookie, "GET", fmt.Sprintf("/estimates/%d", e.ID), 403)
		assertNotContains(t, requestBody(t, web, cookie, "GET", cohort+"&conversion=none", 200), e.Title)
		if cycle == 0 {
			path = fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice)
			fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
			assertContains(t, requestBody(t, web, cookie, "GET", cohort+"&conversion=none", 200), e.Title)
			assertNotContains(t, requestBody(t, web, cookie, "GET", cohort+"&conversion=active", 200), e.Title)
		}
	}
	if invoices[0] == invoices[1] {
		t.Fatal("reconversion reused prior Invoice identity")
	}
	var original time.Time
	var count, active int
	historyCheck(t, pool.QueryRow(ctx, `SELECT created_at FROM estimates WHERE id=$1`, e.ID).Scan(&original))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE reverted_at IS NULL) FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, e.ID).Scan(&count, &active))
	if !original.Equal(created) || count != 2 || active != 1 {
		t.Fatalf("cohort identity/cycles changed: %v %d %d", original, count, active)
	}
	artifact := masterPost(t, web, cookie, "/reports/estimate-cohort/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "created_from": {"2024-03-10"}, "created_to": {"2024-03-10"}}, 303)
	_, err := exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	csvBody := requestBody(t, web, cookie, "GET", artifact+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(csvBody)).ReadAll()
	historyCheck(t, err)
	if len(rows) != 2 {
		t.Fatalf("reconverted source counted per cycle: %v", rows)
	}
	identityFound := false
	for n, column := range rows[0] {
		if column == "estimate_id" && rows[1][n] == fmt.Sprint(e.ID) {
			identityFound = true
		}
	}
	if !identityFound {
		t.Fatalf("cohort CSV lost original identity: %v", rows)
	}
	assertContains(t, csvBody, "2024-03-10")
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/reports/estimate-cohort?scope=A&created_from=2025-01-01", 200), e.Title)
	none := requestBody(t, web, cookie, "GET", cohort+"&conversion=none", 200)
	workReportTotal(t, none, "Distinct Estimates", 0)
	// A contradictory active-conversion summary may be non-actionable, or may
	// navigate to an explicitly empty intersection; it must never broaden.
	if regexp.MustCompile(`<dt>Active conversions \(separate from category\)</dt>\s*<dd>\s*<a[^>]*href="([^"]+)"[^>]*>`).MatchString(none) {
		linked := requestBody(t, web, cookie, "GET", workReportMeasureLink(t, none, "Active conversions (separate from category)"), 200)
		workReportTotal(t, linked, "Distinct Estimates", 0)
		assertNotContains(t, linked, e.Title)
	}
	// A source grant does not confer authority to its active counterpart.
	u := client.User.Create().SetCompanyID(company).SetName("Estimate only reader").SetEmail("cohort-source-only@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Source only", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	reader := sessionCookie(t, ctx, sessions, u.ID)
	assertNotContains(t, requestBody(t, web, reader, "GET", cohort, 200), e.Title)
	expectStatus(t, web, reader, "GET", cohort+"&conversion=active", 403)
	masterPost(t, web, reader, "/reports/estimate-cohort/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "created_from": {"1900-01-01"}, "created_to": {"1900-01-01"}}, 403)
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	sourceGrants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}}
	fullGrants := append(append([]authorization.Grant{}, sourceGrants...), authorization.Grant{Permission: "invoice.view", Scope: authorization.All}, authorization.Grant{Permission: "invoice.export", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Source only", fullGrants, revision)
	historyCheck(t, err)
	readerArtifact := masterPost(t, web, reader, "/reports/estimate-cohort/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	requestBody(t, web, reader, "GET", readerArtifact+"/download", 200)
	_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Source only", sourceGrants, revision+1)
	historyCheck(t, err)
	expectStatus(t, web, reader, "GET", readerArtifact+"/download", 410)
	assertNotContains(t, requestBody(t, web, reader, "GET", cohort, 200), e.Title)
}
