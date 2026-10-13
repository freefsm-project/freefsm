package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestPairingConcurrentRedemptionAndCancellationHTTP(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "redemption", true: "cancellation"}[cancel], func(t *testing.T) {
			pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			api := apiv1.NewRouter(pool, client, sessions)
			body := approvedPairing(t, api, web, sessionCookie(t, ctx, sessions, admin))
			var in map[string]string
			if err := json.Unmarshal([]byte(body), &in); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan int, 8)
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					path, payload := "/pairings/redeem", body
					if cancel && i%2 == 0 {
						path = "/pairings/cancel"
						payload = body
					}
					results <- pairingCall(api, path, payload).Code
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			issued, cancelled := 0, 0
			for code := range results {
				switch code {
				case 201:
					issued++
				case 204:
					cancelled++
				case 409:
				default:
					t.Fatalf("race status=%d", code)
				}
			}
			if issued > 1 || issued > 0 && cancelled > 0 || !cancel && issued != 1 {
				t.Fatalf("issued=%d cancelled=%d", issued, cancelled)
			}
			api = apiv1.NewRouter(pool, client, sessions)
			if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
				t.Fatalf("restart replay=%d", w.Code)
			}
			in["operation_id"] = uuid.NewString()
			changed, _ := json.Marshal(in)
			if w := pairingCall(api, "/pairings/redeem", string(changed)); w.Code != 409 {
				t.Fatal("new operation redeemed consumed code")
			}
			var n, audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE kind='mobile'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.pairing.redeem'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if n != issued || audits != issued {
				t.Fatalf("atomic issuance sessions=%d audit=%d acknowledged=%d", n, audits, issued)
			}
		})
	}
}

func TestPairingExpiryAndAuthorityLossHTTP(t *testing.T) {
	for _, change := range []string{"expiry", "browser-expiry", "browser-revocation", "force", "disable", "password", "restore", "empty-role"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			user := client.User.Create().SetCompanyID(company).SetName("Pairing subject").SetEmail("pairing-subject@test").SetRole("admin").SetPasswordHash("original").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, user.ID)
			api := apiv1.NewRouter(pool, client, sessions)
			body := approvedPairing(t, api, web, cookie)
			var lifetime time.Duration
			var seconds float64
			if err := pool.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-created_at) FROM device_pairings`).Scan(&seconds); err != nil {
				t.Fatal(err)
			}
			lifetime = time.Duration(seconds * float64(time.Second))
			if lifetime < 10*time.Minute || lifetime > 10*time.Minute+time.Second {
				t.Fatalf("documented lifetime=%s", lifetime)
			}
			var err error
			switch change {
			case "expiry":
				_, err = pool.Exec(ctx, `UPDATE device_pairings SET expires_at=clock_timestamp()`)
			case "browser-expiry":
				_, err = pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp() WHERE user_id=$1`, user.ID)
			case "browser-revocation":
				err = sessions.Delete(ctx, cookie.Value)
			case "force":
				_, err = pool.Exec(ctx, `UPDATE users SET force_password_change=true WHERE id=$1`, user.ID)
			case "disable":
				_, err = pool.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, user.ID)
			case "password":
				_, err = pool.Exec(ctx, `UPDATE users SET password_hash='replacement' WHERE id=$1`, user.ID)
			case "restore":
				_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); UPDATE sessions SET revoked_at=clock_timestamp()`)
			case "empty-role":
				svc := authorization.New(pool)
				var role int64
				role, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Empty pairing role", nil)
				if err == nil {
					err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			w := pairingCall(api, "/pairings/redeem", body)
			want := 409
			if change == "empty-role" {
				want = 201
			}
			if w.Code != want {
				t.Fatalf("redeem after %s=%d", change, w.Code)
			}
		})
	}
}

func TestPairingInterruptedWrongBrowserAndAuditFailureHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	api := apiv1.NewRouter(pool, client, sessions)
	first, other := sessionCookie(t, ctx, sessions, admin), sessionCookie(t, ctx, sessions, admin)
	started := pairingCall(api, "/pairings", `{"device_name":"Bound browser","operation_id":"`+uuid.NewString()+`"}`)
	var codes struct {
		Data struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
	}
	if err := json.Unmarshal(started.Body.Bytes(), &codes); err != nil {
		t.Fatal(err)
	}
	body := `{"device_code":"` + codes.Data.DeviceCode + `","operation_id":"` + uuid.NewString() + `"}`
	review := func(cookie *http.Cookie) string {
		w := pairingBrowserPost(web, cookie, "/pair/review", url.Values{"code": {codes.Data.UserCode}})
		match := regexp.MustCompile(`name="review_token" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(match) != 2 {
			t.Fatalf("review=%d", w.Code)
		}
		return match[1]
	}
	token := review(first)
	confirm := url.Values{"review_token": {token}, "device_name": {"Bound browser"}}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatal("interrupted review issued session")
	}
	if w := pairingBrowserPost(web, other, "/pair/approve", confirm); w.Code != 409 {
		t.Fatal("other browser approved")
	}
	replacement := review(first)
	if w := pairingBrowserPost(web, first, "/pair/approve", confirm); w.Code != 409 {
		t.Fatal("old review replayed")
	}
	confirm.Set("review_token", replacement)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_pairing_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'user.pairing.%' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_pairing_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_pairing_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if w := pairingBrowserPost(web, first, "/pair/approve", confirm); w.Code < 400 {
		t.Fatal("approval acknowledged audit failure")
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatal("failed approval granted")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_pairing_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := pairingBrowserPost(web, first, "/pair/approve", confirm); w.Code != 200 {
		t.Fatal("retry approval failed")
	}
	if w := pairingBrowserPost(web, first, "/pair/approve", confirm); w.Code != 409 {
		t.Fatal("approval replay succeeded")
	}
	if _, err = pool.Exec(ctx, `CREATE TRIGGER reject_pairing_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_pairing_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 503 {
		t.Fatal("issuance acknowledged audit failure")
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE kind='mobile'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial issuance=%d %v", n, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_pairing_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 201 {
		t.Fatal("transient failure consumed code")
	}
}

func TestPairingUnauthenticatedApprovalDeniedHTTP(t *testing.T) {
	_, _, web, _, _, _ := permissionHTTPFixture(t)
	for _, path := range []string{"/pair/review", "/pair/approve"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("code=unknown"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthenticated approval=%d", w.Code)
		}
	}
}

func TestPairingExpiryDuringCommitHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	api := apiv1.NewRouter(pool, client, sessions)
	body := approvedPairing(t, api, web, sessionCookie(t, ctx, sessions, admin))
	_, err := pool.Exec(ctx, `CREATE FUNCTION delay_pairing_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.pairing.redeem' THEN PERFORM pg_sleep(0.2); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_pairing_commit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION delay_pairing_commit(); UPDATE device_pairings SET expires_at=clock_timestamp()+interval '100 milliseconds'`)
	if err != nil {
		t.Fatal(err)
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatalf("expired during commit: %d", w.Code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE kind='mobile'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired effect persisted: %d %v", count, err)
	}
}

func TestPairingCancellationOperationHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	api := apiv1.NewRouter(pool, client, sessions)
	body := approvedPairing(t, api, web, sessionCookie(t, context.Background(), sessions, admin))
	if w := pairingCall(api, "/pairings/cancel", body); w.Code != 204 {
		t.Fatalf("cancel operation=%d", w.Code)
	}
	if w := pairingCall(api, "/pairings/cancel", body); w.Code != 204 {
		t.Fatalf("cancel replay=%d", w.Code)
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatal("cancelled pairing redeemed")
	}
}

func TestPairingCancellationAuditFailureHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	api := apiv1.NewRouter(pool, client, sessions)
	body := approvedPairing(t, api, web, sessionCookie(t, ctx, sessions, admin))
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_pair_cancel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.pairing.cancel' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_pair_cancel BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_pair_cancel()`)
	if err != nil {
		t.Fatal(err)
	}
	if w := pairingCall(api, "/pairings/cancel", body); w.Code != 503 {
		t.Fatalf("cancel audit failure acknowledged=%d", w.Code)
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 201 {
		t.Fatalf("failed cancellation consumed approval=%d", w.Code)
	}
}
