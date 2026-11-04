package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestLeaseReportInclusiveNoticeAndClampingHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Calendar report Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Calendar report premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Calendar report equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Calendar retained Asset").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Calendar report agreement", StartsOn: "2024-01-01", EndsOn: "2024-01-30", PremisesID: &l.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(5000)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, company).Scan(&today))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: today}))
	for _, test := range []struct{ at, state, renewal string }{{"2024-01-30", "active", ""}, {"2024-01-31", "month_to_month", "2024-02-28"}, {"2024-02-29", "month_to_month", "2024-03-30"}, {"2024-03-31", "month_to_month", "2024-04-29"}} {
		body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+test.at, 200)
		assertContains(t, body, "Lifecycle: "+test.state)
		if test.renewal != "" {
			assertContains(t, body, test.renewal)
		}
		assertContains(t, body, "Calendar retained Asset")
	}
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, authorization.LeaseNoticeInput{Action: "create", Category: "customer_ordinary", Initiator: "customer", NoticeOn: "2024-01-01", Reason: "Thirty civil days", OperationKey: newOperationKey()}))
	for _, test := range []struct{ at, state string }{{"2024-02-28", "month_to_month"}, {"2024-02-29", "terminated"}} {
		body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+test.at, 200)
		assertContains(t, body, "Lifecycle: "+test.state)
		assertContains(t, body, "Inclusive termination date: 2024-02-28")
		if test.state == "terminated" {
			assertNotContains(t, body, "Calendar retained Asset")
		} else {
			assertContains(t, body, "Calendar retained Asset")
		}
	}
	api := actor
	api.Channel = "api"
	if _, err := svc.LeaseReport(ctx, api, authorization.LeaseReportQuery{}); err == nil {
		t.Fatal("field API exposed Lease report")
	}
	token, err := sessions.CreateMobile(ctx, admin, "Report denial device")
	historyCheck(t, err)
	apiRouter := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewLeaseDirectory(objectref.NewEntDirectory(client), pool), t.TempDir(), 0))
	r := httptest.NewRequest("GET", "/leases/report?as_of=2024-02-28", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	apiRouter.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("field API report status: %d %s", w.Code, w.Body.String())
	}
	assertNotContains(t, w.Body.String(), "Calendar report agreement")
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/report?as_of=2024-02-28&asset=%d", asset.ID), 200)
}

func TestLeaseReportSettlementAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice int64
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, company).Scan(&today))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice))
	_, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "report-payment-provenance-0001"}, InvoiceID: invoice, AmountCents: 106, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertContains(t, body, "Payment: 1.06 on "+today)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{}
	for _, permission := range []string{"lease.view", "lease.billing.view", "invoice.view", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, actor, 0, "Report no settlements", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Invoice report reader").SetEmail("report-invoice74@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
	body = requestBody(t, web, sessionCookie(t, ctx, sessions, user.ID), "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertNotContains(t, body, "Payment:")
}
