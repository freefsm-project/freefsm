package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5/pgxpool"
)

type archivedWorkHistory struct {
	job, project, customer, location, contact, asset int64
	tables                                           []string
}

// The same production lifecycle journey is captured before encrypted backup.
// No mail worker runs: the Delivery remains a durable, unhanded request.
func exerciseWorkArchiveHistory(t *testing.T, pool *pgxpool.Pool, client *ent.Client, web http.Handler, cookie *http.Cookie, company int64) archivedWorkHistory {
	t.Helper()
	ctx := context.Background()
	status := func(key string) int64 {
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, key).Scan(&id))
		return id
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive history owner").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Retained site").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Retained contact").SaveX(ctx)
	at := client.AssetType.Create().SetCompanyID(company).SetName("Archive equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(at.ID).SetName("Retained asset").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(status("project:completed")).SetCompletionPercentage(37).SetName("Retained Project").SaveX(ctx)
	start := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetLocationID(loc.ID).SetCustomerContactID(contact.ID).SetAssetID(a.ID).SetStatusID(status("job:completed")).SetStartTime(start).SetEndTime(start.Add(time.Hour)).SetJobType("Retained Job").SaveX(ctx)
	jobPath := fmt.Sprintf("/jobs/%d", j.ID)
	projectPath := fmt.Sprintf("/projects/%d", p.ID)
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Retained paid work"}, "line_items": {`[{"title":"Recorded work","quantity":1,"unit_price":10}]`}, "invoice_date": {"2026-01-02"}, "tax_rate": {"0"}}, 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE job_id=$1`, j.ID).Scan(&invoice))
	invoicePath := fmt.Sprintf("/invoices/%d", invoice)
	fieldPost(t, web, cookie, invoicePath+"/finalize", nil, 303)
	fieldPost(t, web, cookie, invoicePath+"/payments-with-credit", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"10.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='localhost',smtp_from='archive@example.test' WHERE company_id=$1`, company)
	historyCheck(t, err)
	fieldPost(t, web, cookie, invoicePath+"/pdf/save", nil, 303)
	var pdf int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM document_pdf_versions WHERE document_type='invoice' AND document_id=$1`, invoice).Scan(&pdf))
	fieldPost(t, web, cookie, invoicePath+"/email", url.Values{"pdf_version": {fmt.Sprint(pdf)}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Retained delivery"}, "body": {"Recorded content"}}, 303)
	fieldPost(t, web, cookie, jobPath+"/clock-in", url.Values{"latitude": {"51.5"}, "longitude": {"-0.12"}, "accuracy": {"4"}, "captured_at": {time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)}}, 303)
	dependencies := []string{"invoices", "invoice_payments", "payment_invoice_allocations", "document_pdf_versions", "document_deliveries", "document_delivery_events", "time_entries", "time_entry_history", "time_clock_gps"}
	before := historyDurableRows(t, pool, append([]string{"jobs"}, dependencies...))
	fieldPost(t, web, cookie, projectPath+"/delete", nil, 303)
	historyEqual(t, "Project archive preserves independently operational scheduled Jobs and history", historyDurableRows(t, pool, append([]string{"jobs"}, dependencies...)), before)
	// A finished but unarchived Job remains actionable beneath archived Project.
	fieldPost(t, web, cookie, jobPath, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Still operational"}}, 303)
	fieldPost(t, web, cookie, jobPath+"/delete", nil, 303)
	for _, table := range dependencies {
		historyEqual(t, "Job archive leaves "+table+" unchanged", historyDurableRows(t, pool, []string{table})[table], before[table])
	}
	var queued bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT state='queued' FROM document_deliveries WHERE document_type='invoice' AND document_id=$1`, invoice).Scan(&queued))
	if !queued {
		t.Fatal("Job archive canceled active-document Delivery")
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/customers/%d/contacts/%d/delete", c.ID, contact.ID), nil, 303)
	fieldPost(t, web, cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/delete", a.ID), nil, 303)
	tables := append(dependencies, "customers", "jobs", "projects", "customer_contacts", "locations", "assets", "job_contacts", "job_asset_links", "statuses", "authorization_roles", "authorization_grants", "activity_logs")
	return archivedWorkHistory{j.ID, p.ID, c.ID, loc.ID, contact.ID, a.ID, tables}
}

func TestWorkArchivePreservesIndependentHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	h := exerciseWorkArchiveHistory(t, pool, client, web, cookie, company)
	before := historyDurableRows(t, pool, []string{"customer_contacts", "locations", "assets", "job_contacts", "job_asset_links"})
	for _, kind := range []string{"job", "project"} {
		id := h.job
		if kind == "project" {
			id = h.project
		}
		fieldPost(t, web, cookie, fmt.Sprintf("/%ss/%d/restore", kind, id), nil, 303)
	}
	historyEqual(t, "Restore leaves historical masters and links untouched", historyDurableRows(t, pool, []string{"customer_contacts", "locations", "assets", "job_contacts", "job_asset_links"}), before)
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT js.category_key='job:completed' AND ps.category_key='project:completed' AND j.location_id=$3 AND j.customer_contact_id=$4 AND j.asset_id=$5 AND j.project_id=p.id AND p.location_id=$3 AND p.completion_percentage=37 FROM jobs j JOIN projects p ON p.id=$2 JOIN statuses js ON js.id=j.status_id JOIN statuses ps ON ps.id=p.status_id WHERE j.id=$1`, h.job, h.project, h.location, h.contact, h.asset).Scan(&retained))
	if !retained {
		t.Fatal("Restore rewrote status, progress or historical relationships")
	}
}

func TestWorkArchiveReopenRepairsMastersWithoutRestoringProjectHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	h := exerciseWorkArchiveHistory(t, pool, client, web, cookie, company)
	jobPath := fmt.Sprintf("/jobs/%d", h.job)
	projectPath := fmt.Sprintf("/projects/%d", h.project)
	var jobNew, projectNew int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:new' AND is_category_default`, company).Scan(&jobNew))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:new' AND is_category_default`, company).Scan(&projectNew))
	fieldPost(t, web, cookie, jobPath+"/restore", nil, 303)
	// Each remaining archived operational master independently blocks reopen.
	for _, repair := range []string{"contact", "location", "asset"} {
		masterPost(t, web, cookie, jobPath+"/status", url.Values{"status_id": {fmt.Sprint(jobNew)}}, 409)
		switch repair {
		case "contact":
			masterPost(t, web, cookie, jobPath+"/contacts", url.Values{}, 303)
		case "location":
			fieldPost(t, web, cookie, jobPath, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(h.job)}, "customer_id": {fmt.Sprint(h.customer)}, "job_type": {"Repaired work"}, "location_id": {"0"}}, 303)
		case "asset":
			var link int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1 AND asset_id=$2 AND unlinked_at IS NULL`, h.job, h.asset).Scan(&link))
			fieldPost(t, web, cookie, jobPath+"/assets", url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
		}
	}
	masterPost(t, web, cookie, jobPath+"/status", url.Values{"status_id": {fmt.Sprint(jobNew)}}, 303)
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT j.project_id=p.id AND p.deleted_at IS NOT NULL AND j.deleted_at IS NULL FROM jobs j JOIN projects p ON p.id=$2 WHERE j.id=$1`, h.job, h.project).Scan(&retained))
	if !retained {
		t.Fatal("reopen lost or restored historical Project")
	}
	fieldPost(t, web, cookie, "/jobs", url.Values{"customer_id": {fmt.Sprint(h.customer)}, "job_type": {"Forbidden archived selection"}, "project_id": {fmt.Sprint(h.project)}}, 409)
	fieldPost(t, web, cookie, projectPath+"/restore", nil, 303)
	masterPost(t, web, cookie, projectPath+"/status", url.Values{"status_id": {fmt.Sprint(projectNew)}}, 409)
	masterPost(t, web, cookie, projectPath, url.Values{"customer_id": {fmt.Sprint(h.customer)}, "name": {"Repaired Project"}, "location_id": {"0"}}, 303)
	masterPost(t, web, cookie, projectPath+"/status", url.Values{"status_id": {fmt.Sprint(projectNew)}}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", projectPath, 200), "37%")
}
