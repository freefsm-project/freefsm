package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRefundReversalCommitAuthorityAndAuditHTTP(t *testing.T) {
	for _, action := range []string{"credit.refund", "credit.refund.reverse", "payment.reverse"} {
		t.Run(action, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ac := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Refund authority fence").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			documentTransactionRequest(t, web, ac, "POST", base+"/payments", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
			path := base + "/credit/refunds"
			v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"10.00"}, "method": {"cash"}, "effective_date": {"2026-01-03"}, "reason": {"error"}}
			if action == "credit.refund.reverse" {
				documentTransactionRequest(t, web, ac, "POST", path, v, 303)
				var id string
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM credit_refunds`).Scan(&id))
				path += "/" + id + "/reverse"
			} else if action == "payment.reverse" {
				var id string
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments`).Scan(&id))
				path = base + "/payments/" + id + "/reverse"
				v.Set("date", "2026-01-03")
			}
			v.Set("idempotency_key", newOperationKey())
			auth := authorization.New(pool)
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: action, Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Refund correction", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("fence@test").SetName("Refund correction").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			snapshot := func() string {
				var raw string
				historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM credit_refunds f),(SELECT jsonb_agg(to_jsonb(a) ORDER BY refund_id,credit_id) FROM credit_refund_allocations a),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM settlement_reversals r),(SELECT jsonb_agg(to_jsonb(i) ORDER BY operation,idempotency_key) FROM settlement_idempotency i))::text`).Scan(&raw))
				return raw
			}
			before := snapshot()
			_, err = pool.Exec(ctx, `CREATE FUNCTION refund_correction_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id=`+fmt.Sprint(u.ID)+` THEN RAISE EXCEPTION 'injected refund correction audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refund_correction_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION refund_correction_audit_failure()`)
			historyCheck(t, err)
			documentTransactionRequest(t, web, cookie, "POST", path, v, 500)
			if snapshot() != before {
				t.Fatal("failed audit changed financial facts")
			}
			documentTransactionRequest(t, web, cookie, "GET", "/refund-operations/"+v.Get("idempotency_key"), nil, 404)
			_, err = pool.Exec(ctx, `DROP TRIGGER refund_correction_audit_failure ON authorization_audit; DROP FUNCTION refund_correction_audit_failure()`)
			historyCheck(t, err)
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			var blocker int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
			_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, action)
			historyCheck(t, err)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				web.ServeHTTP(w, r)
				done <- w
			}()
			commercialWaiters(t, ctx, pool, blocker, 1)
			historyCheck(t, tx.Commit(ctx))
			w := <-done
			if w.Code != 403 {
				t.Fatalf("revoked action=%d %s", w.Code, w.Body.String())
			}
			if snapshot() != before {
				t.Fatal("revoked action changed financial facts")
			}
			var admission int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, v.Get("idempotency_key")).Scan(&admission))
			if admission != 0 {
				t.Fatal("failed intent admitted")
			}
		})
	}
}
