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

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetainedGlobalFieldsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	_, client, router, sessions, company, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,object_type,name,field_type,options,conversion_key) VALUES(9600,'estimate','Global source reading','select','["North","South"]','browser-global'),(9601,'invoice','Global invoice reading','select','["North","South"]','browser-global')`); err != nil {
			t.Fatal(err)
		}
	})
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Global browser customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/retained-global-fields.cjs", server.URL, cookie.Value, fmt.Sprint(customer.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
