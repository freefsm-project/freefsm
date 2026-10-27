package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func archiveDraftFixture(t *testing.T, pool *pgxpool.Pool, client *ent.Client, company int64) *ent.Invoice {
	t.Helper()
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive eligibility").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:draft'`, company).Scan(&status))
	return client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetSettlementState("paid").SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SetTitle("Zero-total Draft").SaveX(ctx)
}

func TestCommercialArchiveRejectsEffectivelyDraftHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	i := archiveDraftFixture(t, pool, client, company)
	var effective, settlement, before string
	historyCheck(t, pool.QueryRow(ctx, `SELECT s.category_key,i.settlement_state,to_jsonb(i)::text FROM invoices i JOIN invoice_effective_status e ON e.invoice_id=i.id JOIN statuses s ON s.id=e.status_id WHERE i.id=$1`, i.ID).Scan(&effective, &settlement, &before))
	if effective != "invoice:draft" || settlement != "paid" {
		t.Fatalf("fixture effective=%s settlement=%s", effective, settlement)
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/invoices/%d/delete", i.ID), url.Values{}, 409)
	var after string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, i.ID).Scan(&after))
	if before != after {
		t.Fatal("rejected archive changed Invoice")
	}
}

func TestCommercialArchiveCommitRejectsEffectivelyDraft(t *testing.T) {
	pool, client, _, _, company, _ := permissionHTTPFixture(t)
	ctx := context.Background()
	i := archiveDraftFixture(t, pool, client, company)
	_, err := pool.Exec(ctx, `UPDATE invoices SET deleted_at=now() WHERE id=$1`, i.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "invoice archive requires paid or void" {
		t.Fatalf("expected effective-status archive rejection, got %v", err)
	}
}

func TestCommercialArchiveBlocksIndirectJobReassignmentHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical owner").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained work").SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Archived proposal").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/delete", e.ID), nil, 303)
	before := historyDurableRows(t, pool, []string{"jobs", "estimates", "authorization_audit", "activity_logs"})
	fieldPost(t, web, cookie, fmt.Sprintf("/jobs/%d", j.ID), url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(next.ID)}, "job_type": {"Reassigned work"}, "replace_customer": {"true"}}, 409)
	historyEqual(t, "archived Estimate must block indirect reassignment atomically", historyDurableRows(t, pool, []string{"jobs", "estimates", "authorization_audit", "activity_logs"}), before)
}

func TestCommercialArchiveEveryEstimateStatusRetainsHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Estimate lifecycle").SaveX(ctx)
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, action := range []string{"view", "archive", "archived.view", "restore"} {
		grants = append(grants, authorization.Grant{Permission: "estimate." + action, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent archive custodian", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("archive-custodian@test").SetName("Archive custodian").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	for _, category := range []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"} {
		var status int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, "estimate:"+category).Scan(&status))
		e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetTitle("Retained " + category).SetTerms("Recorded terms").SetValidThrough(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
		path := fmt.Sprintf("/estimates/%d", e.ID)
		var before, after string
		query := `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text FROM estimates d WHERE id=$1`
		historyCheck(t, pool.QueryRow(ctx, query, e.ID).Scan(&before))
		fieldPost(t, web, cookie, path+"/delete", nil, 303)
		if _, err := pool.Exec(ctx, `UPDATE estimates SET notes='ordinary archived edit' WHERE id=$1`, e.ID); err == nil {
			t.Fatal("archived content was writable")
		}
		historyCheck(t, pool.QueryRow(ctx, query, e.ID).Scan(&after))
		if before != after {
			t.Fatalf("%s archive changed history", category)
		}
		fieldPost(t, web, cookie, path+"/restore", nil, 303)
		historyCheck(t, pool.QueryRow(ctx, query, e.ID).Scan(&after))
		if before != after {
			t.Fatalf("%s restore changed history", category)
		}
	}
}

func TestCommercialArchiveEditCommitOrdersHTTP(t *testing.T) {
	for _, archiveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(archiveFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Edit race owner").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Before archive"}, "line_items": {"[]"}, "tax_rate": {"0"}}
			fieldPost(t, web, cookie, "/estimates", values, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates WHERE customer_id=$1`, c.ID).Scan(&id))
			path := fmt.Sprintf("/estimates/%d", id)
			values = commercialReviewedValues(t, web, cookie, path, values)
			values.Set("title", "Accepted before archive")
			archive := creditCommand{path: path + "/delete"}
			edit := creditCommand{path: path, values: values}
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			var archived, edited *httptest.ResponseRecorder
			if archiveFirst {
				archived, edited = f.ordered(t, archive, edit)
			} else {
				edited, archived = f.ordered(t, edit, archive)
			}
			if archived.Code != 303 {
				t.Fatalf("archive=%d %s", archived.Code, archived.Body.String())
			}
			wantTitle := "Accepted before archive"
			if archiveFirst {
				wantTitle = "Before archive"
				if edited.Code != 403 && edited.Code != 409 {
					t.Fatalf("archived edit=%d %s", edited.Code, edited.Body.String())
				}
			} else if edited.Code != 303 {
				t.Fatalf("edit first=%d %s", edited.Code, edited.Body.String())
			}
			var title, audited string
			var closed bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT title,deleted_at IS NOT NULL FROM estimates WHERE id=$1`, id).Scan(&title, &closed))
			historyCheck(t, pool.QueryRow(ctx, `SELECT detail->'after'->>'title' FROM authorization_audit WHERE action='estimate.archive' AND target_id=$1`, id).Scan(&audited))
			if title != wantTitle || audited != wantTitle || !closed {
				t.Fatalf("commit order lost: title=%s audit=%s archived=%v", title, audited, closed)
			}
		})
	}
}

func TestCommercialArchiveOutstandingAndPaidFinancialHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Financial archive").SaveX(ctx)
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Financial history"}, "line_items": {`[{"title":"Recorded service","quantity":1,"unit_price":10}]`}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE customer_id=$1`, c.ID).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
	fieldPost(t, web, cookie, path+"/finalize", nil, 303)
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
	for n := 0; n < 2; n++ {
		fieldPost(t, web, cookie, path+"/payments-with-credit", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"5.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
		if n == 0 {
			fieldPost(t, web, cookie, path+"/delete", nil, 409)
		}
	}
	before := historyDurableRows(t, pool, []string{"invoice_payments", "payment_invoice_allocations"})
	var document, restored string
	query := `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text FROM invoices d WHERE id=$1`
	historyCheck(t, pool.QueryRow(ctx, query, id).Scan(&document))
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	historyCheck(t, pool.QueryRow(ctx, query, id).Scan(&restored))
	if document != restored {
		t.Fatal("Paid archive/restore changed financial projection, manual Status, number or content")
	}
	historyEqual(t, "Paid archive/restore preserves every receipt and allocation", historyDurableRows(t, pool, []string{"invoice_payments", "payment_invoice_allocations"}), before)
}
