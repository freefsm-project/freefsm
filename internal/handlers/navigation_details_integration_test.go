package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestNavigationDetailAffordancesAndFacetsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("DETAIL PRIVATE CUSTOMER").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Navigation Project").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Navigation Asset").SaveX(ctx)
	lines := `[{"title":"DETAIL PRIVATE PRICING","quantity":1,"unit_price":123.45}]`
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(project.ID).SetAssetID(asset.ID).SetJobType("Navigation Job").SetLineItems(lines).SaveX(ctx)
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle("Navigation Estimate").SetLineItems(lines).SetTaxRate("7.25").SaveX(ctx)
	var draft int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:draft' LIMIT 1`, company).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetStatusID(draft).SetTitle("Navigation Invoice").SetInvoiceNumber(1).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetLineItems(lines).SetTaxRate("7.25").SaveX(ctx)
	records := []struct {
		typ string
		id  int64
	}{{"project", project.ID}, {"asset", asset.ID}, {"job", job.ID}, {"estimate", estimate.ID}, {"invoice", invoice.ID}}
	var grants []authorization.Grant
	for _, record := range records {
		grants = append(grants, authorization.Grant{Permission: record.typ + ".view", Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Detail readers", grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"tech", "dispatcher"} {
		u := client.User.Create().SetCompanyID(company).SetEmail(legacy + "-detail@test").SetName("Detail reader").SetPasswordHash("hash").SetRole(legacy).SaveX(ctx)
		if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		for _, record := range records {
			t.Run(legacy+"/"+record.typ, func(t *testing.T) {
				path := fmt.Sprintf("/%ss/%d", record.typ, record.id)
				body := requestBody(t, router, cookie, "GET", path, 200)
				for _, hidden := range []string{`href="` + path + `/edit"`, `action="` + path + `/delete"`, `action="` + path + `/status"`, `href="` + path + `/pdf/preview"`, "DETAIL PRIVATE PRICING", "$123.45", "DETAIL PRIVATE CUSTOMER", "Record Payment", "Create Invoice", "Create Estimate", "Create Next Occurrence", "Clock In to Job", "Grand Total:", "Amount Due:"} {
					if strings.Contains(body, hidden) {
						t.Errorf("view-only detail disclosed %q", hidden)
					}
				}
				expectStatus(t, router, cookie, "GET", path+"/edit", 403)
				expectStatus(t, router, cookie, "GET", path+"/activity", 200)
			})
		}
	}
	for _, record := range records {
		grants = append(grants, authorization.Grant{Permission: record.typ + ".edit", Scope: authorization.All})
	}
	grants = append(grants, authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Detail editors", grants, 1); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='tech-detail@test'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user)
	for _, record := range records {
		path := fmt.Sprintf("/%ss/%d", record.typ, record.id)
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, `href="`+path+`/edit"`)
		editor := requestBody(t, router, cookie, "GET", path+"/edit", 200)
		assertNotContains(t, editor, "DETAIL PRIVATE PRICING")
		assertNotContains(t, editor, "123.45")
	}
	if os.Getenv("BROWSER_TESTS") == "1" {
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
		server := httptest.NewServer(mux)
		defer server.Close()
		var paths []string
		for _, record := range records {
			paths = append(paths, fmt.Sprintf("/%ss/%d", record.typ, record.id))
		}
		data, _ := json.Marshal(paths)
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "node", "testdata/browser/navigation-details.cjs", server.URL, cookie.Value, string(data), t.TempDir()).CombinedOutput()
		t.Logf("detail navigation browser: %s", output)
		if err != nil {
			t.Fatal(err)
		}
	}
	grants = append(grants, authorization.Grant{Permission: "pricing.view", Scope: authorization.All})
	grants = append(grants, authorization.Grant{Permission: "estimate.provenance.view", Scope: authorization.All}, authorization.Grant{Permission: "invoice.provenance.view", Scope: authorization.All})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Price viewers", grants, 2); err != nil {
		t.Fatal(err)
	}
	for _, record := range records[2:] {
		body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/%ss/%d", record.typ, record.id), 200)
		assertContains(t, body, "DETAIL PRIVATE PRICING")
		assertContains(t, body, "$123.45")
		assertNotContains(t, body, "Record Payment")
		assertNotContains(t, body, "Amount Due:")
		expectStatus(t, router, cookie, "GET", fmt.Sprintf("/%ss/%d/activity", record.typ, record.id), 200)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=estimate&object_id=%d&conversion=1", estimate.ID), 200)
	for _, record := range records {
		grants = append(grants, authorization.Grant{Permission: record.typ + ".archived.view", Scope: authorization.All})
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Archived detail viewers", grants, 3); err != nil {
		t.Fatal(err)
	}
	client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	client.Estimate.UpdateOneID(estimate.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	var void int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' LIMIT 1`, company).Scan(&void); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).TransitionInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice.ID, void); err != nil {
		t.Fatal(err)
	}
	client.Invoice.UpdateOneID(invoice.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	for _, record := range records[2:] {
		path := fmt.Sprintf("/%ss/%d", record.typ, record.id)
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, "Archived on")
		assertContains(t, body, "$123.45")
		assertNotContains(t, body, `href="`+path+`/edit"`)
		assertNotContains(t, body, `action="`+path+`/status"`)
	}
}

func TestNavigationDetailIndependentSettlementHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Financial Customer").SaveX(ctx)
	var issued int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced' LIMIT 1`, company).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Independent financial facets").SetStatusID(issued).SetInvoiceNumber(1).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetLineItems(`[{"title":"PRIVATE CAPTURED LINE","quantity":1,"unit_price":41.23}]`).SaveX(ctx)
	_, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "navigation-detail-payment"}, InvoiceID: i.ID, AmountCents: 1234, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02")), Reference: "PRIVATE PAYMENT REFERENCE"})
	if err != nil {
		t.Fatal(err)
	}
	grants := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent detail facets", grants)
	if err != nil {
		t.Fatal(err)
	}
	var users []int64
	for _, legacy := range []string{"tech", "dispatcher"} {
		u := client.User.Create().SetCompanyID(company).SetEmail(legacy + "-financial-details@test").SetName("Financial viewer").SetPasswordHash("hash").SetRole(legacy).SaveX(ctx)
		if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		users = append(users, u.ID)
		body := requestBody(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", fmt.Sprintf("/invoices/%d", i.ID), 200)
		for _, hidden := range []string{"PRIVATE CAPTURED LINE", "$41.23", "PRIVATE PAYMENT REFERENCE", "$12.34", "Record Payment", "Amount Due:"} {
			assertNotContains(t, body, hidden)
		}
	}
	grants = append(grants, authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.financial.view", Scope: authorization.All}, authorization.Grant{Permission: "settlement.view", Scope: authorization.All})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Settlement readers", grants, 1); err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		body := requestBody(t, router, sessionCookie(t, ctx, sessions, user), "GET", fmt.Sprintf("/invoices/%d", i.ID), 200)
		assertContains(t, body, "PRIVATE PAYMENT REFERENCE")
		assertContains(t, body, "$12.34")
		for _, hidden := range []string{"PRIVATE CAPTURED LINE", "$41.23", "Record Payment", "Amount Due:", "Apply Customer Credit", "payments/"} {
			assertNotContains(t, body, hidden)
		}
	}
}

func TestNavigationDetailPricingIntersectsAssignedScopeHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Context parent").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Pricing scope Job").SaveX(ctx)
	linked := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle("Assigned pricing").SetLineItems(`[{"title":"AUTHORIZED PRICE","quantity":1,"unit_price":73.21}]`).SaveX(ctx)
	standalone := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Visible without pricing").SetLineItems(`[{"title":"PRIVATE PRICE","quantity":1,"unit_price":98.76}]`).SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "All Estimates assigned pricing", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("assigned-detail-pricing@test").SetName("Price viewer").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(job.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", linked.ID), 200)
	assertContains(t, body, "$73.21")
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", standalone.ID), 200)
	assertContains(t, body, "Visible without pricing")
	assertNotContains(t, body, "$98.76")
	assertNotContains(t, body, "PRIVATE PRICE")
	client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", linked.ID), 200)
	assertContains(t, body, "Assigned pricing")
	assertNotContains(t, body, "$73.21")
	assertNotContains(t, body, "$0.00")
}
