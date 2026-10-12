package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

// In particular, a nested transaction waiting for its caller's authority lock
// must fail this test promptly rather than hang the integration package.
func documentTransactionRequest(t *testing.T, router http.Handler, cookie *http.Cookie, method, path string, values url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := httptest.NewRequest(method, path, strings.NewReader(values.Encode())).WithContext(ctx)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { router.ServeHTTP(w, r); close(done) }()
	select {
	case <-done:
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	case <-ctx.Done():
		t.Fatalf("%s %s exceeded bounded transaction timeout: %v", method, path, ctx.Err())
		return nil
	}
}

// Snapshot durable effects, not PostgreSQL sequences (which intentionally do
// not roll back). This also catches escaped selection and field audit evidence.
func documentTransactionSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
	 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
	 'estimates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM estimates x),
	 'numbers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY document_type,number) FROM issued_document_numbers x),
	 'settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM company_settings x),
	 'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x),
	 'activity',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM activity_logs x)
	)::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestIntegrationDocumentTransactionCreationHTTP(t *testing.T) {
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			documentTransactionRequest(t, router, adminCookie, "POST", "/settings/custom-fields", url.Values{"name": {"Required reading"}, "object_type": {kind}, "field_type": {"number"}, "precision": {"2"}, "required": {"true"}}, 303)
			var fieldID int64
			if err := pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE object_type=$1 AND name='Required reading'`, kind).Scan(&fieldID); err != nil {
				t.Fatal(err)
			}
			fieldKey := fmt.Sprintf("custom_field_%d", fieldID)
			u := client.User.Create().SetCompanyID(company).SetName("Explicit document author").SetEmail("document-author@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			auth := authorization.New(pool)
			var grants []authorization.Grant
			for _, permission := range []string{kind + ".create", kind + ".view", kind + ".pdf.generate", kind + ".pdf.download", "customer.view", "pricing.view", "item.view", "pricebook.view"} {
				grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
			}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Explicit document author", grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Document customer").SaveX(ctx)
			// A NULL-company Item is the shared catalog, not a foreign tenant.
			item := client.Item.Create().SetName("Shared catalog item").SetSku("SHARED-DOC").SetUnitPrice(23.45).SetUnitCost(987654.32).SaveX(ctx)
			values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Retained document title"}, "notes": {"Retained document notes"}, "tax_rate": {"0"}, "invoice_date": {"2026-10-12"}, "due_date": {"2026-11-12"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Captured shared work","quantity":2,"unit_price":17.25}]`, item.ID)}}
			path := "/" + kind + "s"
			before := documentTransactionSnapshot(t, ctx, pool)
			for _, invalid := range []string{"", "not-a-number"} {
				values.Set(fieldKey, invalid)
				body := documentTransactionRequest(t, router, cookie, "POST", path, values, 422).Body.String()
				for _, retained := range []string{"Retained document title", "Retained document notes", "Captured shared work", "Required reading"} {
					assertContains(t, body, retained)
				}
				if invalid != "" {
					assertContains(t, body, invalid)
				}
				assertNotContains(t, body, "987654.32")
				if got := documentTransactionSnapshot(t, ctx, pool); got != before {
					t.Fatal("invalid required typed field left durable effects")
				}
			}
			values.Set(fieldKey, "12.50")
			documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
			var id int64
			var fields, lines, label string
			if err := pool.QueryRow(ctx, `SELECT id,custom_fields::text,line_items::text,number_label FROM `+kind+`s`).Scan(&id, &fields, &lines, &label); err != nil {
				t.Fatal(err)
			}
			if label == "" {
				t.Fatal("creation did not reserve a document identity")
			}
			assertContains(t, fields, "12.50")
			var captured []services.LineItem
			if err := json.Unmarshal([]byte(lines), &captured); err != nil || len(captured) != 1 || captured[0].ItemID != item.ID || captured[0].UnitPrice != 17.25 || captured[0].Quantity != 2 {
				t.Fatalf("captured selection changed: %s (%v)", lines, err)
			}
			assertNotContains(t, lines, "cost")
			var selections int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='item' AND target_id=$1 AND action='pricebook.view' AND actor_id=$2`, item.ID, u.ID).Scan(&selections); err != nil || selections != 1 {
				t.Fatalf("selection audit count=%d want exactly one: %v", selections, err)
			}
			detailPath := fmt.Sprintf("%s/%d", path, id)
			body := documentTransactionRequest(t, router, cookie, "GET", detailPath, nil, 200).Body.String()
			assertContains(t, body, "12.50")
			assertContains(t, body, "17.25")
			assertNotContains(t, body, "987654.32")
			// Generate is independently authorized; this role has no file.upload.
			documentTransactionRequest(t, router, cookie, "POST", detailPath+"/pdf/save", nil, 303)
			pdf := documentTransactionRequest(t, router, cookie, "GET", detailPath+"/pdf", nil, 200)
			if !strings.HasPrefix(pdf.Body.String(), "%PDF-") {
				t.Fatal("authenticated Generate did not publish downloadable PDF bytes")
			}
			// A captured occurrence is valid above, but an archived Item cannot
			// enter a new document, even with an otherwise complete typed form.
			if err := auth.SetItemArchived(ctx, authorization.Actor{UserID: admin}, item.ID, true); err != nil {
				t.Fatal(err)
			}
			before = documentTransactionSnapshot(t, ctx, pool)
			documentTransactionRequest(t, router, cookie, "POST", path, values, 409)
			if got := documentTransactionSnapshot(t, ctx, pool); got != before {
				t.Fatal("inactive Item rejection left document/number/field/selection effects")
			}
			if err := auth.SetItemArchived(ctx, authorization.Actor{UserID: admin}, item.ID, false); err != nil {
				t.Fatal(err)
			}
			// Fail the OUTER details audit, after the document, number reservation,
			// typed values, selection audit and inner creation audit were written.
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_document_details_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type IN ('invoice','estimate') AND NEW.detail ? 'custom_fields_after' THEN RAISE EXCEPTION 'injected immutable details audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_document_details_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_document_details_audit()`); err != nil {
				t.Fatal(err)
			}
			before = documentTransactionSnapshot(t, ctx, pool)
			values.Set("title", "Must roll back entire creation")
			values.Set(fieldKey, "99.25")
			// Keep the rollback assertion independent of response mapping.
			t.Run("late audit failure status", func(t *testing.T) {
				documentTransactionRequest(t, router, cookie, "POST", path, values, http.StatusConflict)
			})
			if got := documentTransactionSnapshot(t, ctx, pool); got != before {
				t.Fatal("late immutable audit failure escaped atomic document/number/fields/selection rollback")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_document_details_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
		})
	}
}

func TestIntegrationDocumentTransactionJobMoveFence(t *testing.T) {
	for _, command := range []string{"create", "pdf"} {
		t.Run(command, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fence customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Fence job").SaveX(ctx)
			values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Fenced estimate"}, "tax_rate": {"0"}, "line_items": {"[]"}}
			path := "/estimates"
			if command == "pdf" {
				documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
				e := client.Estimate.Query().OnlyX(ctx)
				path = fmt.Sprintf("/estimates/%d/pdf/save", e.ID)
				values = nil
			}
			fence, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(context.Background())
			var blocker int
			if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) boundary`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			moveDone := make(chan error, 1)
			start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
			go func() {
				_, err := services.NewJobService(client).Move(ctx, job.ID, admin, start, start.Add(time.Hour))
				moveDone <- err
			}()
			waiters := func(want int) {
				t.Helper()
				for {
					var n int
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, blocker).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n >= want {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatal("commands did not reach authority fence")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			waiters(1)
			// This is the decisive lock-order assertion: Move is already waiting
			// for authority and must not hold the row needed by a document/PDF.
			if _, err := fence.Exec(ctx, `SELECT id FROM jobs WHERE id=$1 FOR UPDATE NOWAIT`, job.ID); err != nil {
				t.Fatalf("Job Move locked Job before authority fence: %v", err)
			}
			docDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				docDone <- w
			}()
			waiters(2)
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-moveDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Job Move did not finish after authority release")
			}
			select {
			case w := <-docDone:
				want := http.StatusSeeOther
				if command == "pdf" {
					// Move queued first: its assignment/schedule change invalidates
					// the source snapshot rendered before PDF's commit fence.
					want = http.StatusConflict
				}
				if w.Code != want {
					t.Fatalf("document %s after concurrent Move: %d want %d: %s", command, w.Code, want, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal("document command did not finish after authority release")
			}
			if got := client.Job.GetX(ctx, job.ID); got.StartTime == nil || !got.StartTime.Equal(start) {
				t.Fatal("successful concurrent Move did not persist schedule")
			}
			if command == "pdf" {
				var versions int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&versions); err != nil || versions != 0 {
					t.Fatalf("stale concurrent PDF published %d versions: %v", versions, err)
				}
				documentTransactionRequest(t, router, cookie, "POST", path, nil, 303)
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&versions); err != nil || versions != 1 {
					t.Fatalf("fresh PDF retry published %d versions: %v", versions, err)
				}
			} else if n := client.Estimate.Query().CountX(ctx); n != 1 {
				t.Fatalf("concurrent creation persisted %d estimates", n)
			}
		})
	}
}
