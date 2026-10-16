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
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestOperationBatchRollbackAuthorityHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch failure").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Private target").SaveX(ctx)
	user := client.User.Create().SetCompanyID(company).SetName("Assigned batch user").SetEmail("batch80@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Batch grants", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, user.ID, "Batch failure")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	var revision string
	if err := pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	op := uuid.NewString()
	body := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":"job.field_notes","target":%q,"depends_on":[],"notes_revision":%q,"field_notes":"retained original","occurred_at":"2026-10-01T10:00:00Z"}]}`, op, fmt.Sprint(job.ID), revision)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION batch_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.field_notes.edit' THEN RAISE EXCEPTION 'batch audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER batch_audit_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION batch_audit_fault()`); err != nil {
		t.Fatal(err)
	}
	w := batchRequest(api, token, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"retry_after_seconds":5`) || strings.Contains(w.Body.String(), `"receipt"`) {
		t.Fatalf("rollback outcome %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, op).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back admission %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER batch_audit_fault ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	w = batchRequest(api, token, body)
	if !strings.Contains(w.Body.String(), `"code":"accepted"`) {
		t.Fatalf("same ID retry %s", w.Body.String())
	}
	var original map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &original)
	// Immutable occurrence and original target remain in the registry intent hash;
	// neither a changed body nor a route bypass can mutate the accepted command.
	changed := strings.Replace(body, "retained original", "silent rewrite", 1)
	w = batchRequest(api, token, changed)
	assertContains(t, w.Body.String(), "operation_intent_changed")
	if _, err := pool.Exec(ctx, `UPDATE job_execution_receipts SET code='accepted' WHERE operation_id=$1`, op); err == nil {
		t.Fatal("terminal receipt mutable")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/job-execution-operations/"+op, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	receipt := httptest.NewRecorder()
	api.ServeHTTP(receipt, r)
	if receipt.Code != 200 || !strings.Contains(receipt.Body.String(), `"code":"accepted"`) || strings.Contains(receipt.Body.String(), "Private target") || strings.Contains(receipt.Body.String(), "retained original") {
		t.Fatalf("scope loss safe receipt %d %s", receipt.Code, receipt.Body.String())
	}
	// Same-intent replay needs no target comparison and returns the original receipt.
	w = batchRequest(api, token, body)
	var replay map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &replay)
	originalData, _ := json.Marshal(original["data"])
	replayData, _ := json.Marshal(replay["data"])
	if string(originalData) != string(replayData) {
		t.Fatal("scope loss changed receipt")
	}
	newBody := strings.Replace(body, op, uuid.NewString(), 1)
	w = batchRequest(api, token, newBody)
	assertContains(t, w.Body.String(), `"code":"not_found"`)
	if strings.Contains(w.Body.String(), "retained original") {
		t.Fatal("denial leaked comparison")
	}
	// A real transaction holds the authority fence while the HTTP request starts;
	// removing current assignment before release prevents the queued effect.
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019);`); err != nil {
		t.Fatal(err)
	}
	raceOp := uuid.NewString()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- batchRequest(api, token, strings.Replace(body, op, raceOp, 1)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid=190019)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP did not reach authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		assertContains(t, w.Body.String(), `"code":"not_found"`)
	case <-time.After(5 * time.Second):
		t.Fatal("blocked HTTP did not complete")
	}
}

func TestOperationBatchEnvelopeDenialsHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "Envelope")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, body := range []string{`null`, `{}`, `{"operations":null}`, `{"Operations":[]}`, `{"operations":[],"operations":[]}`, `{"operations":[],"extra":true}`, `{"operations":[null]}`, `{"operations":[]} {}`} {
		w := batchRequest(api, token, body)
		if w.Code != 400 {
			t.Fatalf("invalid envelope %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := batchRequest(api, "", `{"operations":[]}`)
	if w.Code != 401 {
		t.Fatalf("anonymous batch%d", w.Code)
	}
	// Sync budget is per whole envelope. Earlier malformed requests consume it
	// too, but an authenticated rate-limited request is never partially admitted.
	for i := 0; i < 20; i++ {
		w = batchRequest(api, token, `{"operations":[]}`)
		if w.Code == 429 {
			break
		}
	}
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("batch throttle %d %s", w.Code, w.Body.String())
	}
}
