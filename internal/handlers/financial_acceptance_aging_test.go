package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestFinancialAcceptanceAgingBoundariesAndSummaryContributorRevocation(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE 'America/New_York')::date`).Scan(&today))
	statuses := map[string]int64{}
	for _, key := range []string{"invoiced", "draft", "void"} {
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key=$1 LIMIT 1`, "invoice:"+key).Scan(&id))
		statuses[key] = id
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Aging contributor").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned source").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Summary reader").SetEmail("summary@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Summary contributors", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "customer.financial.export", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(u.ID).SaveX(ctx)
	want := map[string]string{}
	for i, edge := range []struct {
		days   int
		bucket string
	}{{-1, "not_yet_due"}, {0, "not_yet_due"}, {1, "1_30"}, {30, "1_30"}, {31, "31_60"}, {60, "31_60"}, {61, "61_90"}, {90, "61_90"}, {91, "91_plus"}, {999, "no_due_date"}} {
		due := today.AddDate(0, 0, -edge.days)
		if edge.days == 999 {
			due = time.Time{}
		}
		inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetInvoiceNumber(int64(i + 1)).SetInvoiceDate(today).SetDueDate(due).SetStatusID(statuses["invoiced"]).SetTitle(fmt.Sprintf("Boundary %d", edge.days)).SetLineItems(`[{"title":"Exact cents","quantity":1,"unit_price":1.01}]`).SaveX(ctx)
		want[fmt.Sprint(inv.ID)] = edge.bucket
	}
	for i, kind := range []string{"draft", "void", "zero", "paid", "archived", "inaccessible"} {
		status := statuses[kind]
		if status == 0 {
			status = statuses["invoiced"]
		}
		b := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(int64(20 + i)).SetInvoiceDate(today).SetDueDate(today).SetStatusID(status).SetTitle("Excluded " + kind).SetLineItems(`[{"title":"Excluded","quantity":1,"unit_price":50}]`)
		if kind != "inaccessible" {
			b.SetJobID(job.ID)
		}
		if kind == "zero" {
			b.SetLineItems(`[]`)
		}
		if kind == "archived" {
			b.SetDeletedAt(time.Now())
		}
		inv := b.SaveX(ctx)
		if kind == "paid" {
			_, err := settlement.New(pool).RecordInvoicePaymentWithCredit(ctx, authorization.Actor{UserID: admin, Channel: "web"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: inv.ID, AmountCents: 5000, Method: settlement.Cash, ReceivedDate: settlement.Date(today.Format(time.DateOnly))})
			historyCheck(t, err)
		}
	}
	// Unapplied Credit is independent of the receivable total.
	_, err = settlement.New(pool).RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 99999, Method: settlement.Cash, ReceivedDate: settlement.Date(today.Format(time.DateOnly))})
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	paths := []string{}
	for _, view := range []string{"invoices", "customers"} {
		route := "/reports/invoice-aging/export"
		if view == "customers" {
			route = "/reports/customer-financials/export"
		}
		path := financialAcceptancePost(t, web, cookie, route, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}, 303)
		_, err := exports.Product(pool).ProcessOne(ctx)
		historyCheck(t, err)
		rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
		seen := 0
		total := false
		for _, row := range rows {
			if row["row_type"] == "invoice" {
				seen++
				if want[row["invoice_id"]] != row["bucket"] || row["outstanding_amount"] != "1.01" {
					t.Fatalf("unexpected aging row: %v", row)
				}
			}
			if row["row_type"] == "grand_total" {
				total = true
				if row["invoice_count"] != "10" || row["outstanding_amount"] != "10.10" {
					t.Fatalf("receivables total=%v", row)
				}
			}
		}
		if !total || view == "invoices" && seen != len(want) {
			t.Fatalf("missing population: seen=%d totals=%v", seen, total)
		}
		paths = append(paths, path)
	}
	// The Customer CSV has no Invoice ID column. Its hidden contributors must
	// nevertheless revoke the complete artifact when their assignment is lost.
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, u.ID)
	historyCheck(t, err)
	for _, path := range paths {
		body := requestBody(t, web, cookie, "GET", path+"/download", 410)
		assertNotContains(t, body, "Aging contributor")
		assertNotContains(t, body, "10.10")
		status := requestBody(t, web, cookie, "GET", path, 200)
		assertNotContains(t, status, "Aging contributor")
	}
}
