package handlers

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestSealedEmailPolicyHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPInstanceFixture(t)
	ctx := context.Background()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := admissionManager(t, pool, control)
	startup, err := manager.BeginStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = emailpolicy.Import(ctx, tx, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	control.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		if err := manager.RefreshConnections(); err != nil {
			return false, err
		}
		p, err := emailpolicy.Read(ctx, pool, false)
		return p.Enabled, err
	})
	if err = startup.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	router := control.Middleware(middleware.Company(services.NewCompanySettingsService(client))(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}, control)))
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings/mail", 200)
	if !strings.Contains(body, `action="/settings/enable-email"`) {
		t.Fatal("missing enable form")
	}
	p, err := emailpolicy.Read(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, p.Review()) {
		t.Fatal("enable form lacks current policy review")
	}
	post := func(id, revision string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/settings/enable-email", strings.NewReader(url.Values{"save_action_id": {id}, "save_revision": {revision}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post("", p.Review()); w.Code == 303 {
		t.Fatal("missing action accepted")
	}
	if w := post(newOperationKey(), "stale"); w.Code == 303 {
		t.Fatal("stale review accepted")
	}
	id := newOperationKey()
	w := post(id, p.Review())
	if w.Code != 303 || w.Header().Get("X-FreeFSM-Disposition") != "saved" {
		t.Fatalf("enable %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if control.EmailDisabled() {
		t.Fatal("confirmed database policy not visible")
	}
	if w := post(id, p.Review()); w.Code != 409 {
		t.Fatalf("duplicate dispatched %d", w.Code)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM restore_email_policy`); err != nil {
		t.Fatal(err)
	}
	if !control.EmailDisabled() {
		t.Fatal("missing modern policy fell back to absent legacy marker")
	}
}
