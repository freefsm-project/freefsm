package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOfflineNotesLeaseDefaultHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "Offline Notes")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("POST", "/offline/lease", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Operation-ID", "79000000-0000-4000-8000-000000000001")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("offline lease: %d %s", w.Code, w.Body.String())
	}
	assertContains(t, w.Body.String(), `"expires_at"`)
}

func TestOfflineNotesLeaseConcurrencyAndDenialHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent offline issuance")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	op := uuid.NewString()
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/offline/lease", strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Operation-ID", op)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			responses <- w
		}()
	}
	wg.Wait()
	close(responses)
	var first string
	for w := range responses {
		if w.Code != 200 {
			t.Fatalf("concurrent lease %d %s", w.Code, w.Body.String())
		}
		if first == "" {
			first = w.Body.String()
		} else if first != w.Body.String() {
			t.Fatal("same-ID issuance changed window")
		}
	}
	var receipts, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM offline_authorization_receipts WHERE operation_id=$1),(SELECT count(*) FROM authorization_audit WHERE action='offline.authorization.issue' AND detail->>'operation_id'=$1::text)`, op).Scan(&receipts, &audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || audits != 1 {
		t.Fatalf("duplicate issuance receipt/audit: %d %d", receipts, audits)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("offline-denied@test").SetName("No grants").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Offline denied", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	if err = auth.SaveConfiguration(ctx, authorization.Actor{UserID: user.ID}, "offline", 0, url.Values{"offline_hours": {"72"}}); err == nil {
		t.Fatal("nonprotected role changed sealed policy")
	}
	denied, err := sessions.CreateMobile(ctx, user.ID, "Denied device")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/offline/lease", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+denied)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Operation-ID", uuid.NewString())
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("no Job View issuance: %d %s", w.Code, w.Body.String())
	}
}

func TestOfflineNotesPolicyReplayAndAtomicityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Offline policy")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body, op string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if op != "" {
			r.Header.Set("Operation-ID", op)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) authorization.OfflineAuthorization {
		t.Helper()
		var envelope struct {
			Data authorization.OfflineAuthorization `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	op := uuid.NewString()
	first := decode(call("POST", "/offline/lease", "{}", op, 200))
	if first.IssuedAt == nil || first.ExpiresAt == nil || first.ExpiresAt.Sub(*first.IssuedAt) != 24*time.Hour {
		t.Fatalf("default lease: %+v", first)
	}
	replay := decode(call("POST", "/offline/lease", "{}", op, 200))
	if !replay.ExpiresAt.Equal(*first.ExpiresAt) || replay.ID != first.ID {
		t.Fatal("retry renewed lease")
	}
	receipt := decode(call("GET", "/offline-authorization-operations/"+op, "", "", 200))
	if receipt.ID != first.ID {
		t.Fatal("receipt mismatch")
	}
	call("POST", "/time-entries/lease", "{}", op, 409)
	if _, err := pool.Exec(ctx, `UPDATE offline_authorization_receipts SET receipt=receipt WHERE operation_id=$1`, op); err == nil {
		t.Fatal("offline issuance receipt was mutable")
	}
	if decode(call("GET", "/offline-authorization-operations/"+op, "", "", 200)).ID != first.ID {
		t.Fatal("cross-family misuse changed original issuance")
	}
	set := func(hours string, want int) {
		t.Helper()
		var rev int64
		if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM company_settings WHERE company_id=$1`, company).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		fieldPost(t, router, cookie, "/settings", url.Values{"section": {"offline"}, "revision": {fmt.Sprint(rev)}, "offline_hours": {hours}}, want)
	}
	set("72", 303)
	max := decode(call("POST", "/offline/lease", "{}", uuid.NewString(), 200))
	if max.ExpiresAt.Sub(*max.IssuedAt) != 72*time.Hour {
		t.Fatal("maximum lease")
	}
	// Invalid values reject rather than silently clamping or extending authority.
	var revision int64
	pool.QueryRow(ctx, `SELECT configuration_revision FROM company_settings WHERE company_id=$1`, company).Scan(&revision)
	if err := authorization.New(pool).SaveConfiguration(ctx, authorization.Actor{UserID: admin}, "offline", revision, url.Values{"offline_hours": {"73"}}); err == nil {
		t.Fatal("accepted >72 hours")
	}
	set("0", 303)
	call("POST", "/offline/lease", "{}", uuid.NewString(), 403)
	assertContains(t, call("GET", "/offline/policy", "", "", 200).Body.String(), `"enabled":false`)
	// Immutable old issuance is not renewed by replay under disabled policy.
	old := decode(call("POST", "/offline/lease", "{}", op, 200))
	if old.ID != first.ID {
		t.Fatal("rewrote old issuance")
	}
	set("24", 303)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION offline_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='offline.authorization.issue' THEN RAISE EXCEPTION 'offline audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER offline_audit_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION offline_audit_fault()`); err != nil {
		t.Fatal(err)
	}
	failed := uuid.NewString()
	call("POST", "/offline/lease", "{}", failed, 503)
	call("GET", "/offline-authorization-operations/"+failed, "", "", 404)
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_admissions WHERE operation_id=$1)`, failed).Scan(&exists); err != nil || exists {
		t.Fatalf("failed admission survived: %v %v", exists, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER offline_audit_fault ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	call("POST", "/offline/lease", "{}", failed, 200)
	call("POST", "/offline/lease", `{"hours":72}`, uuid.NewString(), 400)
	call("POST", "/offline/lease", "{}", "", 400)
}
