package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestPDFReferenceExplicitRetryAfterReceiptMissHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference retry customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference retry Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Reference retry PDF").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Original reference client")
	historyCheck(t, err)
	other, err := sessions.CreateMobile(ctx, admin, "Different reference client")
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_reference_pdf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected PDF storage outage'; END $$; CREATE TRIGGER fail_reference_pdf BEFORE INSERT ON document_pdf_versions FOR EACH STATEMENT EXECUTE FUNCTION fail_reference_pdf()`)
	historyCheck(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/test-control/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(403)
			return
		}
		var err error
		switch r.URL.Path {
		case "/test-control/repair":
			_, err = pool.Exec(r.Context(), `DROP TRIGGER fail_reference_pdf ON document_pdf_versions`)
		case "/test-control/fault":
			_, err = pool.Exec(r.Context(), `CREATE TRIGGER fail_reference_pdf BEFORE INSERT ON document_pdf_versions FOR EACH STATEMENT EXECUTE FUNCTION fail_reference_pdf()`)
		case "/test-control/revoke-other":
			err = sessions.RevokeMobile(r.Context(), other)
		case "/test-control/epoch":
			_, err = pool.Exec(r.Context(), `UPDATE session_instance SET epoch=gen_random_uuid() WHERE singleton`)
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			t.Errorf("fixture control: %v", err)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", apiv1.NewRouter(pool, client, sessions))
	server := httptest.NewServer(mux)
	defer server.Close()
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(bounded, "node", "../api/v1/testdata/pdf-reference-retry.cjs", server.URL, token, other, fmt.Sprint(i.ID)).CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	var versions, receipts int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&versions))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM pdf_operations WHERE receipt->>'outcome'='accepted'`).Scan(&receipts))
	if versions != 4 || receipts != 4 {
		t.Fatalf("reference retries duplicated or lost effects: versions=%d receipts=%d", versions, receipts)
	}
}
