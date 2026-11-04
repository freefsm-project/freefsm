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
	"github.com/google/uuid"
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
	var clock string
	historyCheck(t, pool.QueryRow(ctx, `SELECT pg_get_functiondef('lease_dependency_today(bigint)'::regprocedure)`).Scan(&clock))
	var yesterday string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (lease_dependency_today($1)-1)::text`, company).Scan(&yesterday))
	// Insert a coherent prior-day generation/status fixture through the public
	// writers. Preserve every inserted historical fact when today's events arrive.
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$;
 CREATE FUNCTION report_prior_recorded() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.recorded_at=clock_timestamp()-interval '1 day'; RETURN NEW; END $$;
 CREATE FUNCTION report_prior_created() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.created_at=clock_timestamp()-interval '1 day'; RETURN NEW; END $$;
 CREATE TRIGGER report_prior_created BEFORE INSERT ON leases FOR EACH ROW EXECUTE FUNCTION report_prior_created();
 CREATE TRIGGER report_prior_created BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION report_prior_created();
 CREATE TRIGGER report_prior_recorded BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION report_prior_recorded();
 CREATE TRIGGER report_prior_recorded BEFORE INSERT ON lease_finalizations FOR EACH ROW EXECUTE FUNCTION report_prior_recorded();
 CREATE TRIGGER report_prior_recorded BEFORE INSERT ON lease_invoice_generations FOR EACH ROW EXECUTE FUNCTION report_prior_recorded();`, yesterday))
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice int64
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice))
	_, err = pool.Exec(ctx, `DROP TRIGGER report_prior_created ON leases; DROP TRIGGER report_prior_created ON invoices; DROP TRIGGER report_prior_recorded ON authorization_audit; DROP TRIGGER report_prior_recorded ON lease_finalizations; DROP TRIGGER report_prior_recorded ON lease_invoice_generations;`+clock)
	historyCheck(t, err)
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, company).Scan(&today))
	payment, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "report-payment-provenance-0001"}, InvoiceID: invoice, AmountCents: 106, Method: settlement.Cash, ReceivedDate: settlement.Date(yesterday)})
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "Recorded generation: 1.06")
	assertContains(t, body, "Payment: 1.06 on "+yesterday)
	assertContains(t, body, "Effective Invoice Status: invoice:paid")
	assertContains(t, body, "Manual Invoice Status: invoice:invoiced")
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
	assertNotContains(t, body, "invoice:paid")
	assertContains(t, body, "Effective Invoice Status unavailable under current Settlement permissions")
	finance := settlement.New(pool)
	financeActor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	_, err = finance.ReversePayment(ctx, financeActor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment.ID, InvoiceID: invoice, EffectiveDate: settlement.Date(today), Reason: "Report reversal provenance"})
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/leases/report?as_of="+today, 200)
	assertContains(t, body, "Reversed on "+today)
	assertContains(t, body, "Effective Invoice Status: invoice:invoiced")
	historical := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+yesterday, 200)
	assertContains(t, historical, "Effective Invoice Status: invoice:paid")
	assertNotContains(t, historical, "Reversed on")
	var customer int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
	actor.Channel = "web"
	receipt, err := finance.RecordCustomerPayment(ctx, actor, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 106, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	historyCheck(t, err)
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, receipt.ID).Scan(&credit))
	application, err := finance.ApplyCredit(ctx, financeActor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 106, EffectiveDate: settlement.Date(today)})
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/leases/report?as_of="+today, 200)
	assertContains(t, body, "Payment-funded Credit application: 1.06 on "+today)
	assertContains(t, body, application.ID.String())
	assertContains(t, body, "Effective Invoice Status: invoice:paid")
	_, err = finance.ReverseCreditApplication(ctx, financeActor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: application.ID, InvoiceID: invoice, EffectiveDate: settlement.Date(today), Reason: "Correct report application"})
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/leases/report?as_of="+today, 200)
	assertContains(t, body, "Payment-funded Credit application: 1.06 on "+today)
	assertContains(t, body, "Effective Invoice Status: invoice:invoiced")
}
