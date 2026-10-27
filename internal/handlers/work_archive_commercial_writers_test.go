package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestJobArchiveLegacyCancellationCannotDeleteHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained cancellation owner").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Retain this identity").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	var before, after string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&before))
	fieldPost(t, web, cookie, path+"/cancel-next-occurrence", nil, 403)
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&after))
	if before != after {
		t.Fatal("legacy cancellation changed retained Job")
	}
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	fieldPost(t, web, cookie, path+"/cancel-next-occurrence", nil, 403)
	fieldPost(t, web, cookie, path+"/create-next-occurrence", nil, 403)
	if client.Job.Query().CountX(ctx) != 1 {
		t.Fatal("legacy occurrence route changed identity inventory")
	}
}

func TestJobArchiveCommercialAnchorCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, archiveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archiveFirst=%v", kind, archiveFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Anchor race owner").SaveX(ctx)
				var finished int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&finished))
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(finished).SetJobType("Anchor race work").SaveX(ctx)
				values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Independent draft"}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}
				fieldPost(t, web, cookie, "/"+kind+"s", values, 303)
				var doc int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s WHERE customer_id=$1`, c.ID).Scan(&doc))
				path := fmt.Sprintf("/%ss/%d", kind, doc)
				if kind == "invoice" {
					values.Set("invoice_number", fmt.Sprint(client.Invoice.GetX(ctx, doc).InvoiceNumber))
				}
				values.Set("job_id", fmt.Sprint(j.ID))
				link := creditCommand{path: path, values: commercialReviewedValues(t, web, cookie, path, values)}
				archive := creditCommand{path: fmt.Sprintf("/jobs/%d/delete", j.ID)}
				var before, after string
				historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(d)::text FROM `+kind+`s d WHERE id=$1`, doc).Scan(&before))
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				var archived, linked *httptest.ResponseRecorder
				if archiveFirst {
					archived, linked = f.ordered(t, archive, link)
				} else {
					linked, archived = f.ordered(t, link, archive)
				}
				var closed bool
				var job *int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM jobs WHERE id=$1`, j.ID).Scan(&closed))
				historyCheck(t, pool.QueryRow(ctx, `SELECT job_id,to_jsonb(d)::text FROM `+kind+`s d WHERE id=$1`, doc).Scan(&job, &after))
				if archiveFirst {
					if archived.Code != 303 || linked.Code != 409 || !closed || job != nil || before != after {
						t.Fatalf("archive first: archive=%d link=%d closed=%v job=%v unchanged=%v: %s", archived.Code, linked.Code, closed, job, before == after, linked.Body.String())
					}
				} else if linked.Code != 303 || archived.Code != 409 || closed || job == nil || *job != j.ID {
					t.Fatalf("link first: archive=%d link=%d closed=%v job=%v: %s", archived.Code, linked.Code, closed, job, linked.Body.String())
				}
			})
		}
	}
}
