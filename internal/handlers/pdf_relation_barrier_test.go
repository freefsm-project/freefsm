package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestPDFAPIRelationChurnWaitsForAcceptedSourceBarrier(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Barrier customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original Job").SaveX(ctx)
	next := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Replacement Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Barrier invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Barrier client")
	historyCheck(t, err)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	var review struct {
		Data struct {
			Token string `json:"source_token"`
		}
	}
	w := call("GET", path+"/source", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &review))
	_, err = pool.Exec(ctx, `CREATE FUNCTION hold_pdf_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.pdf.generate' THEN PERFORM pg_advisory_xact_lock(190055); END IF; RETURN NEW; END $$; CREATE TRIGGER hold_pdf_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION hold_pdf_audit()`)
	historyCheck(t, err)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(ctx)
	_, err = barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(190055)`)
	historyCheck(t, err)
	generated := make(chan *httptest.ResponseRecorder, 1)
	intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, newOperationKey(), j.ID, review.Data.Token)
	go func() { generated <- call("POST", path+"/generate", intent) }()
	deadline := time.Now().Add(5 * time.Second)
	var generatorPID int
	for generatorPID == 0 {
		historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce((SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name=$1 AND l.locktype='advisory' AND l.objid=190055 AND NOT l.granted LIMIT 1),0)`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&generatorPID))
		if time.Now().After(deadline) {
			t.Fatal("generation did not reach audit barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	changed := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `UPDATE invoices SET job_id=$2 WHERE id=$1`, i.ID, next.ID)
		changed <- err
	}()
	for {
		var blocked bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, generatorPID).Scan(&blocked))
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relation writer did not wait for PDF commit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	historyCheck(t, barrier.Commit(ctx))
	w = <-generated
	if w.Code != 200 {
		t.Fatalf("generation=%d %s", w.Code, w.Body.String())
	}
	historyCheck(t, <-changed)
	var receipt struct {
		Data struct {
			Version int64 `json:"version_id"`
		}
	}
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &receipt))
	var anchor int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT (source_snapshot->'document'->>'job_id')::bigint FROM document_pdf_versions WHERE id=$1`, receipt.Data.Version).Scan(&anchor))
	if anchor != j.ID {
		t.Fatal("accepted version used a later Job")
	}
	download := fmt.Sprintf("%s/versions/%d", path, receipt.Data.Version)
	if w := call("GET", download, ""); w.Code != 409 {
		t.Fatalf("stale exact API download=%d", w.Code)
	}
	if w := call("GET", download+"?confirm_stale=1", ""); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF-") {
		t.Fatalf("confirmed retained API=%d", w.Code)
	}
	if w := call("POST", path+"/generate", intent); w.Code != 200 {
		t.Fatal("original receipt replay failed")
	}
	if w := call("POST", path+"/generate", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, newOperationKey(), j.ID, review.Data.Token)); w.Code != 409 {
		t.Fatalf("old original anchor intent=%d", w.Code)
	}
}
