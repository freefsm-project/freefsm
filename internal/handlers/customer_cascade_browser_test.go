package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestCustomerCascadeReviewDenialLostResponseBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser cascade").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, router, adminCookie, base+"/contacts", url.Values{"first_name": {"Private review child"}, "is_primary": {"true"}}, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser custodian", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.archive", Scope: authorization.All}, {Permission: "customer.archived.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}, {Permission: "contact.archive", Scope: authorization.All}})
	historyCheck(t, err)
	denied, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser denied", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("cascade-browser@test").SetName("Custodian").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	web := middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router))))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Test-Lose-Response") == "true" {
			buffer := httptest.NewRecorder()
			web.ServeHTTP(buffer, r)
			if buffer.Code >= 400 && !(buffer.Code == 409 && strings.Contains(buffer.Body.String(), `"disposition":"saved"`)) {
				t.Errorf("lost response was not committed: %d %s", buffer.Code, buffer.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/customer-cascade.cjs", server.URL, cookie.Value, adminCookie.Value, fmt.Sprint(c.ID), fmt.Sprint(u.ID), fmt.Sprint(denied)).CombinedOutput()
	t.Logf("browser: %s", out)
	historyCheck(t, err)
	var archived bool
	var audits, receipts int
	historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,(SELECT count(*) FROM authorization_audit WHERE action='customer.archive' AND target_id=$1),(SELECT count(*) FROM online_save_actions WHERE request_path=$2 AND domain_committed_at IS NOT NULL) FROM customers WHERE id=$1`, c.ID, base+"/delete").Scan(&archived, &audits, &receipts))
	if !archived || audits != 1 || receipts != 1 {
		t.Fatalf("lost response identity: archived=%v audits=%d receipts=%d", archived, audits, receipts)
	}
}
