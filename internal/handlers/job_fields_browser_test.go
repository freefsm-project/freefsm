package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestJobFieldsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser field customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent browser values").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("browser-field@test").SetName("Independent worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	for _, name := range []string{"First reading", "Second reading"} {
		fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {name}, "field_type": {"text"}}, 303)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/job-fields.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
