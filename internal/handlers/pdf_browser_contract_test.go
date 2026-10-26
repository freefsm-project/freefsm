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
)

func TestPDFPersistedChromiumSelectionAndLostResponse(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser PDF customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser PDF Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Browser PDF invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	cookies := []string{sessionCookie(t, ctx, sessions, admin).Value}
	auth := authorization.New(pool)
	for n, permission := range []string{"invoice.pdf.generate", "invoice.pdf.download"} {
		role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, permission, []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: permission, Scope: authorization.All}})
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetEmail(fmt.Sprintf("browser-pdf-%d@test", n)).SetName("PDF independent browser").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
		historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		cookies = append(cookies, sessionCookie(t, ctx, sessions, u.ID).Value)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Browser PDF source edit")
	historyCheck(t, err)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", apiv1.NewRouter(pool, client, sessions)))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, "node", "testdata/browser/pdf-versions.cjs", server.URL, cookies[0], fmt.Sprint(i.ID), token, cookies[1], cookies[2])
	output, err := command.CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	var versions, audits int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions WHERE document_id=$1`, i.ID).Scan(&versions))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='invoice.pdf.download' AND detail->>'stale_confirmed'='true'`).Scan(&audits))
	if versions != 2 || audits != 1 {
		t.Fatalf("browser effects versions=%d stale audits=%d", versions, audits)
	}
}
