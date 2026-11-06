package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommercialZeroDocumentDisplaysAcceptedTotalsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Empty document Customer").SaveX(ctx)
	for _, kind := range []string{"estimate", "invoice"} {
		documentTransactionRequest(t, router, cookie, "POST", "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Empty accepted document"}, "invoice_date": {"2026-10-15"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/%ss/%d", kind, id), 200), "Grand Total: $0.00")
	}
}

func TestCommercialZeroAdjustmentsSurviveMetadataBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recorded zero Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Recorded zero Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Zero adjustment API author")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	ids := map[string]int64{}
	before := map[string]string{}
	for _, kind := range []string{"estimate", "invoice"} {
		body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Recorded zero adjustment","tax_rate":"10","adjustments":{"discount":{"kind":"percentage","value":"0"},"surcharge":{"kind":"fixed","value":"0.00"}},"line_items":[{"title":"One","quantity":"1","unit_price":"0.05","taxable":true,"discount_adjustment":{"kind":"fixed","value":"0.00"},"surcharge_adjustment":{"kind":"percentage","value":"0"}},{"title":"Two","quantity":"1","unit_price":"0.05","taxable":true}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID)
		request := httptest.NewRequest("POST", "/"+kind+"s", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("create %s: %d %s", kind, response.Code, response.Body.String())
		}
		var receipt struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		ids[kind] = receipt.Data.ID
		var raw string
		if err := pool.QueryRow(ctx, `SELECT financial FROM `+kind+`s WHERE id=$1`, receipt.Data.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		before[kind] = raw
	}
	// A later master change must not be applied by an accidental loss of zero
	// adjustment objects while the browser saves only Notes.
	client.Customer.UpdateOneID(c.ID).SetTaxExempt(true).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/commercial-zero-adjustments.cjs", server.URL, cookie.Value, fmt.Sprint(ids["estimate"]), fmt.Sprint(ids["invoice"])).CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatal(err)
	}
	for kind, id := range ids {
		var raw string
		if err := pool.QueryRow(ctx, `SELECT financial FROM `+kind+`s WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != before[kind] {
			t.Fatalf("%s metadata edit rewrote recorded zero adjustments or accepted exemption", kind)
		}
	}
}
