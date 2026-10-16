package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestBoundedPaymentReferencePersistedAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Payment reference Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Payment reference Job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Payment reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Reference payment","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var receipt struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, receipt.Data.ID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "../api/v1/testdata/payment-reference-conformance.cjs", server.URL, token, fmt.Sprint(receipt.Data.ID), fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var cents int64
	if err := pool.QueryRow(ctx, `SELECT count(*),sum(amount_cents) FROM invoice_payments`).Scan(&count, &cents); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cents != 2500 {
		t.Fatalf("reconciliation repeated fixed receipt: count%d cents%d", count, cents)
	}
}
