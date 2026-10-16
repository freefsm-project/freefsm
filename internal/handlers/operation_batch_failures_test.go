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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	var blocker int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	assertForeignAuthorityExcluded(t, ctx, pool, blocker)
	raceOp := uuid.NewString()
	done := make(chan *httptest.ResponseRecorder, 1)
	raceRequest := httptest.NewRequest("POST", "/sync/operations", strings.NewReader(strings.Replace(body, op, raceOp, 1))).WithContext(ctx)
	raceRequest.Header.Set("Authorization", "Bearer "+token)
	raceRequest.Header.Set("Content-Type", "application/json")
	go func() { w := httptest.NewRecorder(); api.ServeHTTP(w, raceRequest); done <- w }()
	waitAuthorityRequest(t, ctx, pool, blocker, authorityWriterStatement)
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

func TestOperationBatchDependencyElementShapeHTTP(t *testing.T) {
	for _, element := range []string{`null`, `42`, `true`, `{}`, `[]`, `"not-a-uuid"`, `""`} {
		t.Run(element, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Envelope atomicity").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original target").SaveX(ctx)
			var revision string
			if err := pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			token, err := sessions.CreateMobile(ctx, admin, "Dependency shape")
			if err != nil {
				t.Fatal(err)
			}
			first, later := uuid.NewString(), uuid.NewString()
			operation := func(id, deps string) string {
				return fmt.Sprintf(`{"operation_id":%q,"type":"job.field_notes","target":%q,"depends_on":%s,"notes_revision":%q,"field_notes":"first valid effect"}`, id, fmt.Sprint(job.ID), deps, revision)
			}
			body := `{"operations":[` + operation(first, `[]`) + `,` + operation(later, `[`+element+`]`) + `]}`
			w := batchRequest(apiv1.NewRouter(pool, client, sessions), token, body)
			isString := strings.HasPrefix(element, `"`)
			wantStatus, wantCount := 400, 0
			if isString {
				wantStatus, wantCount = 200, 2
			}
			if w.Code != wantStatus {
				t.Errorf("shape status=%d want=%d: %s", w.Code, wantStatus, w.Body.String())
			}
			if isString {
				assertContains(t, w.Body.String(), `"code":"invalid_dependency"`)
			}
			for _, table := range []string{"operation_admissions", "job_execution_receipts"} {
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE operation_id IN ($1,$2)`, first, later).Scan(&count); err != nil || count != wantCount {
					t.Errorf("%s count=%d want=%d err=%v", table, count, wantCount, err)
				}
			}
			var notes, currentRevision string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(tech_notes,''), notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&notes, &currentRevision); err != nil {
				t.Fatal(err)
			}
			if !isString && (notes != "" || currentRevision != revision) {
				t.Errorf("malformed later member committed earlier effect: notes=%q revision=%s", notes, currentRevision)
			}
			if isString && notes != "first valid effect" {
				t.Errorf("typed dependency rejection lost earlier effect: %q", notes)
			}
			var audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.field_notes.edit'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			wantAudits := 0
			if isString {
				wantAudits = 1
			}
			if audits != wantAudits {
				t.Errorf("mutation audits=%d want=%d", audits, wantAudits)
			}
		})
	}
}
