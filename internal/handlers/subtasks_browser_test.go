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

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/google/uuid"
	"github.com/justinas/nosurf"
)

func TestSubtaskPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser checklist").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	run, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(run, "node", "testdata/browser/subtasks.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	check, err := authorization.New(pool).Subtasks(ctx, authorization.Actor{UserID: admin}, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Entries) != 1 || check.Entries[0].Title != "Browser valve" {
		t.Fatalf("browser persistence: %+v", check)
	}
	counts := map[string]int{}
	for _, event := range check.History {
		counts[event.Action]++
	}
	if counts["add"] != 2 || counts["rename"] != 1 || counts["reorder"] != 1 || counts["complete"] != 1 || counts["reopen"] != 1 || counts["remove"] != 1 {
		t.Fatalf("browser effects lost/duplicated: %v", counts)
	}
}

func TestSubtaskOfflineReferenceHarness(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference checklist").SaveX(ctx)
	if _, err := authorization.New(pool).SubmitSubtask(ctx, authorization.Actor{UserID: admin}, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Reference row"}); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Subtask executable reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	run, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(run, "node", "testdata/browser/subtasks-reference.cjs", server.URL, token, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
