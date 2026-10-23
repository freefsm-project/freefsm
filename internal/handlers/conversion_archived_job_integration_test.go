package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestConversionCarriesExistingArchivedJobOnlyWithAllRecordsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Retained historical Job").SaveX(ctx)
	fieldPost(t, router, cookie, "/estimates", url.Values{
		"title": {"Carry existing Job"}, "customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "line_items": {"[]"}, "tax_rate": {"0"},
	}, 303)
	var estimateID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Carry existing Job'`).Scan(&estimateID); err != nil {
		t.Fatal(err)
	}
	estimatePath := fmt.Sprintf("/estimates/%d", estimateID)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned conversion", []authorization.Grant{
		{Permission: "estimate.view", Scope: authorization.Assigned},
		{Permission: "estimate.convert", Scope: authorization.Assigned},
		{Permission: "invoice.view", Scope: authorization.Assigned},
		{Permission: "invoice.create", Scope: authorization.Assigned},
	})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("assigned-conversion@test").SetName("Assigned converter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).ExecX(ctx)
	assignedCookie := sessionCookie(t, ctx, sessions, user.ID)
	fieldPost(t, router, assignedCookie, estimatePath+"/convert", url.Values{"idempotency_key": {newOperationKey()}, "revision": {"unauthorized submitted revision"}}, 403)
	var cycles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM estimate_invoice_conversion_cycles`).Scan(&cycles); err != nil || cycles != 0 {
		t.Fatalf("denied conversion produced a cycle: %d %v", cycles, err)
	}
	// The exception follows current grants, not the protected Administrator Role
	// or the legacy role label on this same authenticated Product User.
	allRole, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "All Records conversion", []authorization.Grant{
		{Permission: "estimate.view", Scope: authorization.All},
		{Permission: "estimate.convert", Scope: authorization.All},
		{Permission: "invoice.view", Scope: authorization.All},
		{Permission: "invoice.create", Scope: authorization.All},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, allRole); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, assignedCookie, "GET", estimatePath+"/convert", 200), "Convert Estimate to Invoice")
	converted := fieldPost(t, router, assignedCookie, estimatePath+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoicePath := strings.Split(converted.Header().Get("Location"), "?")[0]
	var invoiceID int64
	if _, err := fmt.Sscanf(invoicePath, "/invoices/%d", &invoiceID); err != nil {
		t.Fatal(err)
	}
	var retainedJob, retainedCustomer int64
	var category string
	var hidden, archived bool
	if err := pool.QueryRow(ctx, `SELECT i.job_id,i.customer_id,s.category_key,e.conversion_hidden_at IS NOT NULL,j.deleted_at IS NOT NULL
		FROM invoices i JOIN statuses s ON s.id=i.status_id JOIN estimates e ON e.id=i.estimate_id JOIN jobs j ON j.id=i.job_id WHERE i.id=$1`, invoiceID).Scan(&retainedJob, &retainedCustomer, &category, &hidden, &archived); err != nil {
		t.Fatal(err)
	}
	if retainedJob != job.ID || retainedCustomer != customer.ID || category != "invoice:draft" || !hidden || !archived {
		t.Fatalf("conversion changed historical anchors or lifecycle: job=%d customer=%d category=%s hidden=%v archived=%v", retainedJob, retainedCustomer, category, hidden, archived)
	}
	// Carrying an existing link is not authority to select an archived Job anew.
	fieldPost(t, router, cookie, "/invoices", url.Values{
		"title": {"New archived selection"}, "customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-10-23"},
	}, 409)
	var invoices int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE job_id=$1`, job.ID).Scan(&invoices); err != nil || invoices != 1 {
		t.Fatalf("new archived selection created an Invoice: %d %v", invoices, err)
	}
}
