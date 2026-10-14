package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func securityRequest(router http.Handler, token, method, path, operation, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if operation != "" {
		r.Header.Set("Operation-ID", operation)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestSecurityIdentitySelfRevocationRecoveryHTTP(t *testing.T) {
	for _, path := range []string{"/session", "/sessions", "/sessions/current"} {
		t.Run(path, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			token, err := sessions.CreateMobile(ctx, admin, "self revoke")
			if err != nil {
				t.Fatal(err)
			}
			if path == "/sessions/current" {
				var id int64
				if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE device_name='self revoke'`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				path = fmt.Sprintf("/sessions/%d", id)
			}
			router := apiv1.NewRouter(pool, client, sessions)
			id := uuid.NewString()
			w := securityRequest(router, token, "DELETE", path, id, "")
			if w.Code != 204 {
				t.Fatalf("self revoke=%d %s", w.Code, w.Body.String())
			}
			for _, request := range []struct{ method, path string }{{"DELETE", path}, {"GET", "/session-operations/" + id}, {"GET", "/me"}} {
				w := securityRequest(router, token, request.method, request.path, id, "")
				if w.Code != 401 {
					t.Fatalf("revoked bearer retained authority: %d %s", w.Code, w.Body.String())
				}
			}
			fresh, err := sessions.CreateMobile(ctx, admin, "fresh authentication")
			if err != nil {
				t.Fatal(err)
			}
			receipt := securityData(t, securityRequest(router, fresh, "GET", "/session-operations/"+id, "", ""), 200)
			if strings.Contains(string(receipt), "self revoke") || strings.Contains(string(receipt), "token") {
				t.Fatalf("sensitive receipt: %s", receipt)
			}
			if w := securityRequest(router, fresh, "DELETE", path, id, ""); w.Code != 409 {
				t.Fatalf("fresh client rebound revoked client's intent: %d %s", w.Code, w.Body.String())
			}
			securityData(t, securityRequest(router, fresh, "GET", "/me", "", ""), 200)
			other := client.User.Create().SetCompanyID(company).SetEmail("other-receipt@test").SetName("Other").SetRole("dispatcher").SetPasswordHash("hash").SaveX(ctx)
			otherToken, err := sessions.CreateMobile(ctx, other.ID, "other")
			if err != nil {
				t.Fatal(err)
			}
			if w := securityRequest(router, otherToken, "GET", "/session-operations/"+id, "", ""); w.Code != 404 {
				t.Fatalf("other user receipt=%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSecurityIdentityCrossFamilyAndConcurrencyHTTP(t *testing.T) {
	for _, order := range []string{"lease-first", "clock-first", "concurrent", "lease-retries"} {
		t.Run(order, func(t *testing.T) {
			pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			token, err := sessions.CreateMobile(ctx, admin, "collision")
			if err != nil {
				t.Fatal(err)
			}
			router := apiv1.NewRouter(pool, client, sessions)
			id := uuid.NewString()
			lease := func() *httptest.ResponseRecorder {
				return securityRequest(router, token, "POST", "/time-entries/lease", id, `{}`)
			}
			clock := func() *httptest.ResponseRecorder {
				return securityRequest(router, token, "POST", "/time-entries/clock-in", "", fmt.Sprintf(`{"operation_id":%q}`, id))
			}
			var first, second *httptest.ResponseRecorder
			switch order {
			case "lease-first":
				first, second = lease(), clock()
			case "clock-first":
				first, second = clock(), lease()
			default:
				var wg sync.WaitGroup
				start := make(chan struct{})
				wg.Add(2)
				go func() { defer wg.Done(); <-start; first = lease() }()
				go func() {
					defer wg.Done()
					<-start
					if order == "lease-retries" {
						second = lease()
					} else {
						second = clock()
					}
				}()
				close(start)
				wg.Wait()
			}
			if order == "lease-retries" {
				a, b := securityData(t, first, 200), securityData(t, second, 200)
				if string(a) != string(b) {
					t.Fatalf("concurrent issuance differed: %s %s", a, b)
				}
			} else if !(first.Code == 200 && second.Code == 409 || first.Code == 409 && second.Code == 200) {
				t.Fatalf("cross family statuses=%d,%d: %s %s", first.Code, second.Code, first.Body.String(), second.Body.String())
			}
			var effects int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM time_clock_leases)+(SELECT count(*) FROM time_entries)`).Scan(&effects); err != nil {
				t.Fatal(err)
			}
			if effects != 1 {
				t.Fatalf("effects=%d", effects)
			}
			// A session mutation cannot reuse either domain's operation ID.
			if w := securityRequest(router, token, "DELETE", "/sessions", id, ""); w.Code != 409 {
				t.Fatalf("session cross-family=%d %s", w.Code, w.Body.String())
			}
			securityData(t, securityRequest(router, token, "GET", "/me", "", ""), 200)
		})
	}
}

func TestSecurityIdentityAuditRollbackHTTP(t *testing.T) {
	for _, family := range []string{"lease", "session"} {
		t.Run(family, func(t *testing.T) {
			pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			token, err := sessions.CreateMobile(ctx, admin, "audit rollback")
			if err != nil {
				t.Fatal(err)
			}
			router := apiv1.NewRouter(pool, client, sessions)
			if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_security_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('time.clock_lease.issue','user.sessions.logout') THEN RAISE EXCEPTION 'security audit unavailable' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_security_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_security_audit()`); err != nil {
				t.Fatal(err)
			}
			id, method, path, body := uuid.NewString(), "POST", "/time-entries/lease", `{}`
			if family == "session" {
				method, path, body = "DELETE", "/session", ""
			}
			if w := securityRequest(router, token, method, path, id, body); w.Code != 503 {
				t.Fatalf("audit failure=%d %s", w.Code, w.Body.String())
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM clock_lease_receipts)+(SELECT count(*) FROM session_revocation_receipts)+(SELECT count(*) FROM time_clock_leases)+(SELECT count(*) FROM sessions WHERE revoked_at IS NOT NULL)`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("audit failure retained effect/admission/receipt: %d", count)
			}
			securityData(t, securityRequest(router, token, "GET", "/me", "", ""), 200)
			if _, err := pool.Exec(ctx, `DROP TRIGGER fail_security_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			want := 200
			if family == "session" {
				want = 204
			}
			if w := securityRequest(router, token, method, path, id, body); w.Code != want {
				t.Fatalf("same-ID recovered=%d %s", w.Code, w.Body.String())
			}
			for _, table := range []string{"clock_lease_receipts", "session_revocation_receipts"} {
				if family == "lease" && table != "clock_lease_receipts" || family == "session" && table != "session_revocation_receipts" {
					continue
				}
				if _, err := pool.Exec(ctx, `UPDATE `+table+` SET receipt='{}' WHERE operation_id=$1`, id); err == nil {
					t.Fatal("receipt was mutable")
				}
			}
		})
	}
}

func TestSecurityIdentityLeaseCurrentAuthorityHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	worker := client.User.Create().SetCompanyID(company).SetEmail("lease-worker@test").SetName("Worker").SetRole("dispatcher").SetPasswordHash("hash").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease worker", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, worker.ID, "authority")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	id := uuid.NewString()
	first := securityData(t, securityRequest(router, token, "POST", "/time-entries/lease", id, `{}`), 200)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Lease worker", nil, 1); err != nil {
		t.Fatal(err)
	}
	if got := securityData(t, securityRequest(router, token, "GET", "/clock-lease-operations/"+id, "", ""), 200); string(got) != string(first) {
		t.Fatalf("receipt lost after grants: %s", got)
	}
	if got := securityData(t, securityRequest(router, token, "POST", "/time-entries/lease", id, `{}`), 200); string(got) != string(first) {
		t.Fatal("replay changed window after grant loss")
	}
	denied := uuid.NewString()
	if w := securityRequest(router, token, "POST", "/time-entries/lease", denied, `{}`); w.Code != 403 {
		t.Fatalf("new lease after grant loss=%d", w.Code)
	}
	got := securityData(t, securityRequest(router, token, "GET", "/clock-lease-operations/"+denied, "", ""), 200)
	if strings.Contains(string(got), `"id"`) || !strings.Contains(string(got), `"code":"forbidden"`) {
		t.Fatalf("denial receipt=%s", got)
	}
	var lease authorization.ClockLease
	if err := json.Unmarshal(first, &lease); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`{"operation_id":%q,"lease_id":%q,"occurred_at":%q}`, uuid.NewString(), lease.ID, time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
	if w := securityRequest(router, token, "POST", "/time-entries/clock-in", "", command); w.Code != 403 {
		t.Fatalf("lease bypassed current authority=%d %s", w.Code, w.Body.String())
	}
	fresh, err := sessions.CreateMobile(ctx, worker.ID, "other client")
	if err != nil {
		t.Fatal(err)
	}
	if w := securityRequest(router, fresh, "GET", "/clock-lease-operations/"+id, "", ""); w.Code != 404 {
		t.Fatalf("other client saw lease=%d", w.Code)
	}
}

func TestSecurityIdentitySessionRevocationHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "acting device")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.CreateMobile(ctx, admin, "target device"); err != nil {
		t.Fatal(err)
	}
	var target int64
	if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE device_name='target device'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	id := uuid.NewString()
	path := fmt.Sprintf("/sessions/%d", target)
	for range 2 {
		w := securityRequest(router, token, "DELETE", path, id, "")
		if w.Code != 204 {
			t.Fatalf("revoke=%d %s", w.Code, w.Body.String())
		}
	}
	receipt := securityData(t, securityRequest(router, token, "GET", "/session-operations/"+id, "", ""), 200)
	if !strings.Contains(string(receipt), `"code":"accepted"`) {
		t.Fatalf("missing receipt: %s", receipt)
	}
	changed := securityRequest(router, token, "DELETE", "/sessions", id, "")
	if changed.Code != 409 {
		t.Fatalf("changed intent=%d %s", changed.Code, changed.Body.String())
	}
	securityData(t, securityRequest(router, token, "GET", "/me", "", ""), 200)
}

func securityData(t *testing.T, w *httptest.ResponseRecorder, want int) json.RawMessage {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestSecurityIdentityLeaseLostResponseHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "lease identity")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	id := uuid.NewString()
	first := securityData(t, securityRequest(router, token, "POST", "/time-entries/lease", id, `{}`), 200)
	// Simulate a response lost after commit: unchanged retry must retain the
	// original lease identity AND original staging window, not renew either.
	retry := securityData(t, securityRequest(router, token, "POST", "/time-entries/lease", id, `{}`), 200)
	if string(first) != string(retry) {
		t.Fatalf("lost-response retry issued a fresh lease: first=%s retry=%s", first, retry)
	}
	receipt := securityData(t, securityRequest(router, token, "GET", "/clock-lease-operations/"+id, "", ""), 200)
	if string(first) != string(receipt) {
		t.Fatalf("receipt changed: %s != %s", first, receipt)
	}
}
