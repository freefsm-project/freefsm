package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"
)

func TestConversionEveryActiveStatusUsesCurrentCompanyDateDefaultsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Date defaults").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained Job").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='Pacific/Kiritimati',default_due_days=7 WHERE company_id=$1`, company)
	historyCheck(t, err)
	var today, offset string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE 'Pacific/Kiritimati')::date::text,((now() AT TIME ZONE 'Pacific/Kiritimati')::date+14)::text`).Scan(&today, &offset))
	for n, category := range []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"} {
		var status int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, "estimate:"+category).Scan(&status))
		defaultValue := ""
		wantDue := "0001-01-01"
		if n >= 3 {
			defaultValue = "14"
			wantDue = offset
		}
		_, err := pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,jsonb_build_object('invoice_due_days',$2::text)) ON CONFLICT(customer_id) DO UPDATE SET defaults=excluded.defaults`, c.ID, defaultValue)
		historyCheck(t, err)
		e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetStatusID(status).SetTitle("Active " + category).SetTerms("Current source terms").SetValidThrough(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
		fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/convert", e.ID), url.Values{"idempotency_key": {newOperationKey()}}, 303)
		var date, due, manual, terms string
		var customer, job int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT i.invoice_date::date::text,i.due_date::date::text,s.category_key,i.customer_id,i.job_id,i.terms FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.estimate_id=$1`, e.ID).Scan(&date, &due, &manual, &customer, &job, &terms))
		if date != today || due != wantDue || manual != "invoice:draft" || customer != c.ID || job != j.ID || terms != "Current source terms" {
			t.Fatalf("%s converted incorrectly: date=%s due=%s status=%s customer=%d job=%d terms=%s", category, date, due, manual, customer, job, terms)
		}
	}
	var count, effects int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(DISTINCT invoice_number),(SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM document_deliveries) FROM invoices`).Scan(&count, &effects))
	if count != 6 || effects != 0 {
		t.Fatalf("numbers=%d unintended settlement/delivery=%d", count, effects)
	}
}

func TestConversionReversionRequiresExplicitInvoiceRestoreAndFreshReviewHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Explicit restore").SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Restore source").SetTerms("Recorded source").SaveX(ctx)
	fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/convert", e.ID), url.Values{"idempotency_key": {newOperationKey()}}, 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, e.ID).Scan(&invoice))
	path := fmt.Sprintf("/invoices/%d", invoice)
	// Zero-total Draft is still effectively Draft; explicitly finalize it before
	// archiving the resulting Paid history.
	fieldPost(t, web, cookie, path+"/finalize", url.Values{}, 303)
	old := conversionReview(t, web, cookie, path+"/revert-to-estimate")
	fieldPost(t, web, cookie, path+"/delete", url.Values{}, 303)
	fieldPost(t, web, cookie, path+"/revert-to-estimate", old, 403)
	fieldPost(t, web, cookie, path+"/restore", url.Values{}, 303)
	old.Set("idempotency_key", newOperationKey())
	fieldPost(t, web, cookie, path+"/revert-to-estimate", old, 409)
	fieldPost(t, web, cookie, path+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
}
