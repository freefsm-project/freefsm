package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestJobArchiveSemanticEligibilityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, category := range []string{"new", "travel_time", "in_progress", "pending", "completed", "canceled"} {
		t.Run(category, func(t *testing.T) {
			var status int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, "job:"+category).Scan(&status))
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Retained work").SaveX(ctx)
			want := 409
			if category == "completed" || category == "canceled" {
				want = 303
			}
			var before, after string
			historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&before))
			fieldPost(t, web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil, want)
			if want == 409 {
				historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&after))
				if before != after {
					t.Fatal("ineligible archive changed Job")
				}
			} else {
				var recorded int
				historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.archive' AND target_id=$1`, j.ID).Scan(&recorded))
				if recorded != 1 {
					t.Fatalf("immutable archive events=%d", recorded)
				}
			}
		})
	}
}

func TestJobArchiveRestoreAuditAtomicHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic Job lifecycle").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Historical finished work").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	for _, action := range []string{"archive", "restore"} {
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION reject_job_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.`+action+`' THEN RAISE EXCEPTION 'injected lifecycle audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_job_lifecycle_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_job_lifecycle_audit()`)
		historyCheck(t, err)
		var before, after string
		query := `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`
		historyCheck(t, pool.QueryRow(ctx, query, j.ID).Scan(&before))
		suffix := "/restore"
		if action == "archive" {
			suffix = "/delete"
		}
		fieldPost(t, web, cookie, path+suffix, nil, 500)
		historyCheck(t, pool.QueryRow(ctx, query, j.ID).Scan(&after))
		if before != after {
			t.Fatal("failed audit changed Job")
		}
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_job_lifecycle_audit ON authorization_audit`)
		historyCheck(t, err)
		fieldPost(t, web, cookie, path+suffix, nil, 303)
		var recordedBefore, recordedAfter string
		historyCheck(t, pool.QueryRow(ctx, `SELECT (detail->'before')::text,(detail->'after')::text FROM authorization_audit WHERE action=$1 AND target_id=$2`, "job."+action, j.ID).Scan(&recordedBefore, &recordedAfter))
		historyCheck(t, pool.QueryRow(ctx, query, j.ID).Scan(&after))
		if recordedBefore != before || recordedAfter != after {
			t.Fatal("audit snapshots differ from committed Job")
		}
	}
}

func TestJobArchiveNewInvoiceCommitOrdersHTTP(t *testing.T) {
	for _, archiveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(archiveFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive race").SaveX(ctx)
			var status int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Race work").SaveX(ctx)
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: sessionCookie(t, ctx, sessions, admin)}
			archive := creditCommand{path: fmt.Sprintf("/jobs/%d/delete", j.ID)}
			create := creditCommand{path: "/invoices", values: url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Racing draft"}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}}
			var archived, created *httptest.ResponseRecorder
			if archiveFirst {
				archived, created = f.ordered(t, archive, create)
			} else {
				created, archived = f.ordered(t, create, archive)
			}
			var closed bool
			var documents, events int
			historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM jobs WHERE id=$1`, j.ID).Scan(&closed))
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE job_id=$1`, j.ID).Scan(&documents))
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.archive' AND target_id=$1`, j.ID).Scan(&events))
			if archiveFirst {
				if archived.Code != 303 || created.Code < 400 || !closed || documents != 0 || events != 1 {
					t.Fatalf("archive first: archive=%d create=%d closed=%v documents=%d audit=%d", archived.Code, created.Code, closed, documents, events)
				}
			} else if created.Code != 303 || archived.Code != 409 || closed || documents != 1 || events != 0 {
				t.Fatalf("document first: archive=%d create=%d closed=%v documents=%d audit=%d", archived.Code, created.Code, closed, documents, events)
			}
		})
	}
}

func TestJobRestoreIndependentAuthorityAndHistoricalLinksHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restore owner").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:canceled' AND is_category_default`, company).Scan(&status))
	var projectStatus int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' AND is_category_default`, company).Scan(&projectStatus))
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(projectStatus).SetName("Historical Project").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetStatusID(status).SetJobType("Retained canceled work").SaveX(ctx)
	fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/projects/%d/delete", p.ID), nil, 303)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/delete", nil, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Job history custodian", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.archived.view", Scope: authorization.All}, {Permission: "job.restore", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("job-restorer@test").SetName("Restorer").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
	historyCheck(t, err)
	assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
	fieldPost(t, web, cookie, path+"/restore", nil, 409)
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=NULL WHERE id=$1`, c.ID)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT j.deleted_at IS NULL AND j.status_id=$2 AND j.project_id=$3 AND p.deleted_at IS NOT NULL FROM jobs j JOIN projects p ON p.id=j.project_id WHERE j.id=$1`, j.ID, status, p.ID).Scan(&retained))
	if !retained {
		t.Fatal("Restore changed finished status or historical Project")
	}
	fieldPost(t, web, cookie, path+"/delete", nil, 403)
}

func TestJobArchiveCommercialEligibilityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commercial dependencies").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	var completed int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&completed))
	for _, state := range []string{"draft", "unpaid", "partial", "paid", "void", "archived"} {
		t.Run("invoice/"+state, func(t *testing.T) {
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(completed).SetJobType("Finished work").SaveX(ctx)
			fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Linked invoice"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":10}]`}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}, 303)
			var invoice int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE job_id=$1`, j.ID).Scan(&invoice))
			path := fmt.Sprintf("/invoices/%d", invoice)
			if state != "draft" {
				fieldPost(t, web, cookie, path+"/finalize", nil, 303)
			}
			if state == "partial" || state == "paid" {
				amount := "10.00"
				if state == "partial" {
					amount = "5.00"
				}
				fieldPost(t, web, cookie, path+"/payments-with-credit", url.Values{"idempotency_key": {newOperationKey()}, "amount": {amount}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
			}
			if state == "void" || state == "archived" {
				var status int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status))
				fieldPost(t, web, cookie, path+"/status", commercialReviewedValues(t, web, cookie, path, url.Values{"status_id": {fmt.Sprint(status)}}), 303)
			}
			if state == "archived" {
				fieldPost(t, web, cookie, path+"/delete", nil, 303)
			}
			want := 303
			if state == "draft" || state == "unpaid" || state == "partial" {
				want = 409
			}
			fieldPost(t, web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil, want)
		})
	}
	for _, category := range []string{"draft", "estimate", "sent", "accepted", "rejected", "completed", "archived", "converted"} {
		t.Run("estimate/"+category, func(t *testing.T) {
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(completed).SetJobType("Finished proposal work").SaveX(ctx)
			key := category
			if key == "archived" || key == "converted" {
				key = "accepted"
			}
			var status int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, "estimate:"+key).Scan(&status))
			e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetStatusID(status).SetTitle("Expired proposal").SetValidThrough(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
			if category == "archived" {
				fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/delete", e.ID), nil, 303)
			}
			if category == "converted" {
				fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/convert", e.ID), url.Values{"idempotency_key": {newOperationKey()}}, 303)
				var invoice int64
				var effective string
				historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,s.category_key FROM invoices i JOIN invoice_effective_status e ON e.invoice_id=i.id JOIN statuses s ON s.id=e.status_id WHERE i.job_id=$1`, j.ID).Scan(&invoice, &effective))
				if effective != "invoice:draft" {
					t.Fatalf("converted effective status=%s", effective)
				}
				fieldPost(t, web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil, 409)
				fieldPost(t, web, cookie, fmt.Sprintf("/invoices/%d/finalize", invoice), nil, 303)
			}
			want := 409
			if category == "rejected" || category == "completed" || category == "archived" || category == "converted" {
				want = 303
			}
			fieldPost(t, web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil, want)
		})
	}
}
