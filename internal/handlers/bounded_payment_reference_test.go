package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

func TestBoundedPaymentReferencePersistedAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Payment reference Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Payment reference Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Payment reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reference payment","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID), 200)
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
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Reference collector", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "payment.record", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	collector := client.User.Create().SetCompanyID(company).SetEmail("reference-collector@test").SetName("Reference collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, collector.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, collector.ID); err != nil {
		t.Fatal(err)
	}
	token, err = sessions.CreateMobile(ctx, collector.ID, "Scoped Payment reference")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", api)
	mux.HandleFunc("/fixture/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		var err error
		switch r.URL.Path {
		case "/fixture/revoke":
			err = auth.RevokeSessions(r.Context(), authorization.Actor{UserID: admin}, collector.ID, 0)
		case "/fixture/remove-payment-grant":
			_, err = auth.SaveRole(r.Context(), authorization.Actor{UserID: admin}, role, "Reference collector", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}}, 1)
		case "/fixture/restore-payment-grant":
			_, err = auth.SaveRole(r.Context(), authorization.Actor{UserID: admin}, role, "Reference collector", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "payment.record", Scope: authorization.Assigned}}, 2)
		case "/fixture/unassign", "/fixture/reassign":
			err = auth.WithinActor(r.Context(), authorization.Actor{UserID: admin}, func(tx pgx.Tx, _ authorization.Principal) error {
				if r.URL.Path == "/fixture/unassign" {
					_, err := tx.Exec(r.Context(), `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, collector.ID)
					return err
				}
				_, err := tx.Exec(r.Context(), `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, j.ID, collector.ID)
				return err
			})
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	server := httptest.NewServer(mux)
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
