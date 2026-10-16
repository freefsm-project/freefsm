package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func batchRequest(api http.Handler, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/sync/operations", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestOperationBatchDependenciesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Dependencies").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original").SaveX(ctx)
	var revision string
	if err := pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Batch dependencies")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	newOp := func(deps ...string) map[string]any {
		if deps == nil {
			deps = []string{}
		}
		return map[string]any{"operation_id": uuid.NewString(), "type": "job.field_notes", "target": fmt.Sprint(job.ID), "depends_on": deps, "notes_revision": revision, "field_notes": ""}
	}
	submit := func(ops ...map[string]any) []struct {
		OperationID string                             `json:"operation_id"`
		Receipt     *authorization.JobExecutionReceipt `json:"receipt"`
		Error       *struct {
			Code string `json:"code"`
		} `json:"error"`
	} {
		body, _ := json.Marshal(map[string]any{"operations": ops})
		w := batchRequest(api, token, string(body))
		if w.Code != 200 {
			t.Fatalf("batch %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Results []struct {
					OperationID string                             `json:"operation_id"`
					Receipt     *authorization.JobExecutionReceipt `json:"receipt"`
					Error       *struct {
						Code string `json:"code"`
					} `json:"error"`
				} `json:"results"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.Results
	}
	a, b := newOp(), newOp()
	a["depends_on"] = []string{b["operation_id"].(string)}
	b["depends_on"] = []string{a["operation_id"].(string)}
	unknown := newOp(uuid.NewString())
	child := newOp(unknown["operation_id"].(string))
	good := newOp()
	after := newOp(good["operation_id"].(string))
	results := submit(a, b, unknown, child, good, after)
	for i, want := range []string{"invalid_dependency", "failed_dependency", "invalid_dependency", "failed_dependency", "accepted", "accepted"} {
		if results[i].Receipt == nil || results[i].Receipt.Code != want {
			t.Fatalf("result%d want%s: %+v", i, want, results[i])
		}
	}
	// Known successful bound receipt outside this envelope is valid; terminal
	// rejection cannot be rehabilitated by changing its dependencies under one ID.
	if out := submit(newOp(good["operation_id"].(string))); out[0].Receipt == nil || out[0].Receipt.Code != "accepted" {
		t.Fatalf("known success: %+v", out)
	}
	leaseID := uuid.NewString()
	leaseRequest := httptest.NewRequest("POST", "/offline/lease", strings.NewReader(`{}`))
	leaseRequest.Header.Set("Authorization", "Bearer "+token)
	leaseRequest.Header.Set("Content-Type", "application/json")
	leaseRequest.Header.Set("Operation-ID", leaseID)
	leaseResponse := httptest.NewRecorder()
	api.ServeHTTP(leaseResponse, leaseRequest)
	if leaseResponse.Code != 200 {
		t.Fatalf("known receipt fixture%d %s", leaseResponse.Code, leaseResponse.Body.String())
	}
	if out := submit(newOp(leaseID)); out[0].Receipt == nil || out[0].Receipt.Code != "accepted" {
		t.Fatalf("known successful shared-registry family: %+v", out)
	}
	changedGood := newOp()
	changedGood["operation_id"] = good["operation_id"]
	changedGood["field_notes"] = "changed intent reuse"
	if out := submit(changedGood, newOp(good["operation_id"].(string))); out[1].Receipt == nil || out[1].Receipt.Code != "failed_dependency" {
		t.Fatalf("changed-intent ancestor must block descendant: %+v", out)
	}
	old := results[0].Receipt
	if out := submit(a); out[0].Receipt == nil || *out[0].Receipt != *old {
		t.Fatalf("terminal replay changed: %+v", out)
	}
	a["depends_on"] = []string{}
	if out := submit(a); out[0].Error == nil || out[0].Error.Code != "operation_intent_changed" {
		t.Fatalf("changed dependency intent: %+v", out)
	}
	// One changed Notes operation is committed before an unrelated stale command.
	first := newOp()
	first["field_notes"] = "first accepted"
	stale := newOp()
	stale["field_notes"] = "later stale"
	out := submit(first, stale)
	if out[0].Receipt == nil || out[0].Receipt.Code != "accepted" || out[1].Receipt == nil || out[1].Receipt.Code != "stale_notes_revision" {
		t.Fatalf("per-operation order/atomicity: %+v", out)
	}
	var notes string
	if err := pool.QueryRow(ctx, `SELECT tech_notes FROM jobs WHERE id=$1`, job.ID).Scan(&notes); err != nil || notes != "first accepted" {
		t.Fatalf("earlier accepted rolled back: %q %v", notes, err)
	}
	// Original receipt remains safe after scope loss; a new session cannot use
	// the old client's success as dependency authority.
	other, err := sessions.CreateMobile(ctx, admin, "Different binding")
	if err != nil {
		t.Fatal(err)
	}
	token = other
	if out := submit(newOp(good["operation_id"].(string))); out[0].Receipt == nil || out[0].Receipt.Code != "invalid_dependency" {
		t.Fatalf("foreign binding: %+v", out)
	}
}

func TestOperationBatchConcurrencyHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent batch").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Concurrent target").SaveX(ctx)
	var revision string
	if err := pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent batch")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	op := uuid.NewString()
	body := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":"job.field_notes","target":%q,"depends_on":[],"notes_revision":%q,"field_notes":"one effect"}]}`, op, fmt.Sprint(job.ID), revision)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- batchRequest(api, token, body) }()
	}
	wg.Wait()
	close(responses)
	var original string
	for w := range responses {
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"accepted"`) {
			t.Fatalf("concurrent %d %s", w.Code, w.Body.String())
		}
		if original == "" {
			original = w.Body.String()
		} else if original != w.Body.String() {
			t.Fatal("immutable receipt changed")
		}
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.field_notes.edit' AND detail->>'operation_id'=$1`, op).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("duplicate audit %d %v", audits, err)
	}
}

func TestOperationBatchBoundariesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch owner").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Batch target").SaveX(ctx)
	var revision string
	if err := pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Batch bounds")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	operation := func() string {
		return fmt.Sprintf(`{"operation_id":%q,"type":"job.field_notes","target":%q,"depends_on":[],"notes_revision":%q,"field_notes":""}`, uuid.NewString(), fmt.Sprint(job.ID), revision)
	}
	for _, count := range []int{101, 100} {
		ops := make([]string, count)
		for i := range ops {
			ops[i] = operation()
		}
		w := batchRequest(api, token, `{"operations":[`+strings.Join(ops, ",")+`]}`)
		want := 200
		if count == 101 {
			want = 413
		}
		if w.Code != want {
			t.Fatalf("%d operations: %d %s", count, w.Code, w.Body.String())
		}
		var receipts int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_execution_receipts WHERE user_id=$1`, admin).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if (count == 101 && receipts != 0) || (count == 100 && receipts != 100) {
			t.Fatalf("partial envelope/receipt count: %d", receipts)
		}
	}
	for _, size := range []int{(1 << 20) + 1, 1 << 20} {
		op := operation()
		body := `{"operations":[` + op + `]}`
		body += strings.Repeat(" ", size-len(body))
		w := batchRequest(api, token, body)
		want := 200
		if size > 1<<20 {
			want = 413
		}
		if w.Code != want {
			t.Fatalf("%d bytes: %d %s", size, w.Code, w.Body.String())
		}
		var id string
		var parsed map[string]any
		_ = json.Unmarshal([]byte(op), &parsed)
		id = parsed["operation_id"].(string)
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_admissions WHERE operation_id=$1)`, id).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != (want == 200) {
			t.Fatal("oversized envelope partially admitted")
		}
	}
}
