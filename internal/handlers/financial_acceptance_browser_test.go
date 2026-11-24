package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestFinancialAcceptancePersistedBrowserFiveExports(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1; browser acceptance not executed")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser financial source").SaveX(ctx)
	var issued int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
	client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99001).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("Browser aging source").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":50}]`).SaveX(ctx)
	_, err := settlement.New(pool).RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Browser finance reader").SetEmail("finance-browser@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser finance without exports", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser/financial-reports.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, u.ID).Value)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
}
