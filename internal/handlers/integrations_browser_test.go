package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestIntegrationsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Mail browser reviewer", []authorization.Grant{{Permission: "configuration.mail.view", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("reviewer-browser@test").SetPasswordHash("hash").SetName("Reviewer").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coordinate browser customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Browser depot").SetAddress1("Reviewed address").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/integrations.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, u.ID).Value, fmt.Sprint(location.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
