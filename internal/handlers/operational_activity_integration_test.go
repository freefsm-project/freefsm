package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOperationalActivityDateActorFiltersHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Activity filter subject").SaveX(ctx)
	for _, event := range []struct{ action, date string }{{"created", "2026-01-01"}, {"updated", "2026-02-01"}, {"restored", "2026-03-01"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,created_at) VALUES($1,$2,$3,'customer',$4,$5::date)`, company, admin, event.action, customer.ID, event.date); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/activity?object_type=customer&object_id=%d&action=updated&actor=%d&from=2026-02-01&until=2026-03-01", customer.ID, admin)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Activity filter subject")
	assertContains(t, body, "Updated at")
	assertNotContains(t, body, ">created</span>")
	assertNotContains(t, body, ">restored</span>")
	body = requestBody(t, router, cookie, "GET", "/activity?from=2026-02-01&until=2026-03-01", 200)
	assertContains(t, body, ">updated</span>")
	assertNotContains(t, body, ">created</span>")
	assertNotContains(t, body, ">restored</span>")
	body = requestBody(t, router, cookie, "GET", "/activity?actor=99999999", 200)
	assertNotContains(t, body, "Activity filter subject")
	requestBody(t, router, cookie, "GET", "/activity?from=not-a-date", 400)
	requestBody(t, router, cookie, "GET", "/activity?actor=-1", 400)
	requestBody(t, router, cookie, "GET", "/activity?from=2026-03-01&until=2026-02-01", 400)
}

func TestOperationalActivityPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser activity subject").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,created_at,metadata) VALUES($1,$2,'created','customer',$3,'2026-01-15T12:00:00Z','{}'),($1,$2,'updated','customer',$3,'2026-02-15T12:00:00Z',jsonb_build_object('before',jsonb_build_object('service_notes','Original observation'),'after',jsonb_build_object('service_notes',$4::text)))`, company, admin, customer.ID, "Multiline observation\n"+strings.Repeat("field_observation_", 24)); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/operational-activity.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAdministrativeHistoryReadAuditFailureWithholdsRowsHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'password_reset','user',$2,'{"entity_name":"Protected history subject"}')`, company, admin); err != nil {
		t.Fatal(err)
	}
	path := "/settings/activity/history?type=user"
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Protected history subject")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='audit.view' AND actor_id=$1`, admin).Scan(&count); err != nil || count != 1 {
		t.Fatalf("administrative history read audit count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_history_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_history_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_history_audit()`); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path, 503)
	assertNotContains(t, body, "Protected history subject")
}

func TestOperationalActivityUnknownHistoricalActorHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Unknown author subject").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,action,object_type,object_id) VALUES($1,'created','customer',$2)`, company, customer.ID); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/activity", 200)
	assertContains(t, body, "Unknown historical actor")
	assertNotContains(t, body, "Historical administrator")
}

func TestOperationalActivityFailurePreservesFiltersHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Failure record").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `ALTER TABLE activity_logs RENAME TO unavailable_activity_logs`); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/activity?from=2026-01-01", 503)
	assertContains(t, body, "Activity could not be loaded")
	assertContains(t, body, "2026-01-01")
	assertContains(t, body, "Refresh activity")
	assertNotContains(t, body, "No activity recorded")
	assertNotContains(t, body, "SQLSTATE")
	if _, err := pool.Exec(ctx, `ALTER TABLE customers RENAME TO unavailable_customers`); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=customer&object_id=%d", c.ID), 503)
	assertContains(t, body, "Activity could not be loaded")
	assertContains(t, body, `value="customer"`)
	assertNotContains(t, body, "SQLSTATE")
}

func TestOperationalActivityExcludesSealedAndRemovedTextHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Safe activity parent").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES
	($1,$2,'comment_added','customer',$3,'{"comment_preview":"REMOVED_COMMENT_SECRET"}'),
	($1,$2,'password_reset','customer',$3,'{"entity_name":"SEALED_VISIBLE_PARENT"}'),
	($1,$2,'password_reset','user',$2,'{"entity_name":"SEALED_ADMIN_EVENT"}')`, company, admin, customer.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/activity", fmt.Sprintf("/activity?object_type=customer&object_id=%d", customer.ID)} {
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, "Safe activity parent")
		assertNotContains(t,body,"reset password")
		for _, secret := range []string{"REMOVED_COMMENT_SECRET", "SEALED_ADMIN_EVENT"} {
			if strings.Contains(body, secret) {
				t.Fatalf("ordinary history discloses %s", secret)
			}
		}
	}
}
