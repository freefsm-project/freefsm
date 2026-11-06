package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type batchCommercialFixture struct {
	pool                         *pgxpool.Pool
	api                          http.Handler
	token, kind, otherToken      string
	job, customer, company, user int64
}

func newBatchCommercialFixture(t *testing.T, kind string) batchCommercialFixture {
	t.Helper()
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Batch job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Batch server")
	historyCheck(t, err)
	otherToken, err := sessions.CreateMobile(ctx, admin, "Other batch client")
	historyCheck(t, err)
	// Long replay matrices use the supported configurable budget. Default batch
	// throttling remains covered by the existing mounted policy HTTP tests.
	return batchCommercialFixture{pool, apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100}), token, kind, otherToken, j.ID, c.ID, company, admin}
}

func TestCommercialBatchTransientPredecessorAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	ctx := context.Background()
	a, b, c := f.create(t), f.create(t), f.create(t)
	parent := f.operation(a, "edit", map[string]any{"notes": "Retry original"})
	child := f.operation(b, "edit", map[string]any{"notes": "Explicit repair only"}, parent["operation_id"].(string))
	unrelated := f.operation(c, "edit", map[string]any{"notes": "Independent"})
	_, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION batch_parent_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation_id='%s'::uuid THEN RAISE EXCEPTION 'parent receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER batch_parent_failure BEFORE INSERT ON commercial_operations FOR EACH ROW EXECUTE FUNCTION batch_parent_failure()`, parent["operation_id"]))
	historyCheck(t, err)
	results := batchResults(t, f, parent, child, unrelated)
	if results[0].Error.Code != "retryable_failure" || results[0].Receipt != nil {
		t.Fatalf("transient parent %+v", results[0])
	}
	batchOutcome(t, results[1], "failed_dependency")
	batchOutcome(t, results[2], "accepted")
	_, err = f.pool.Exec(ctx, `DROP TRIGGER batch_parent_failure ON commercial_operations`)
	historyCheck(t, err)
	batchOutcome(t, batchResults(t, f, parent)[0], "accepted")
	batchOutcome(t, batchResults(t, f, child)[0], "failed_dependency")
	assertContains(t, f.read(t, b.ID), `"notes":""`)
	child["operation_id"] = newOperationKey()
	batchOutcome(t, batchResults(t, f, child)[0], "accepted")
	assertContains(t, f.read(t, b.ID), `"notes":"Explicit repair only"`)
}

func (f batchCommercialFixture) create(t *testing.T) commercialMatrixState {
	t.Helper()
	return matrixState(t, lifecycleCall(t, f.api, f.token, "POST", "/"+f.kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": f.job, "customer_id": f.customer, "title": "Batch original"}), 200))
}

func (f batchCommercialFixture) read(t *testing.T, id int64) string {
	t.Helper()
	return lifecycleCall(t, f.api, f.token, "GET", fmt.Sprintf("/%ss/%d", f.kind, id), "", 200)
}

func (f batchCommercialFixture) operation(state commercialMatrixState, action string, fields map[string]any, deps ...string) map[string]any {
	fields["job_id"], fields["revision"] = f.job, state.Revision
	if deps == nil {
		deps = []string{}
	}
	return map[string]any{"operation_id": newOperationKey(), "type": f.kind + "." + action, "target": fmt.Sprint(state.ID), "depends_on": deps, "commercial": fields}
}

type commercialBatchResult struct {
	OperationID string         `json:"operation_id"`
	Receipt     map[string]any `json:"receipt"`
	Error       struct {
		Code string `json:"code"`
	} `json:"error"`
}

func batchResults(t *testing.T, f batchCommercialFixture, ops ...map[string]any) []commercialBatchResult {
	t.Helper()
	raw := commercialBatchCall(t, f.api, f.token, matrixBody(t, map[string]any{"operations": ops}))
	var envelope struct {
		Data struct {
			Results []commercialBatchResult `json:"results"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal([]byte(raw), &envelope))
	if len(envelope.Data.Results) != len(ops) {
		t.Fatalf("missing ordered results: %s", raw)
	}
	for i, result := range envelope.Data.Results {
		if result.OperationID != ops[i]["operation_id"] {
			t.Fatal("result order changed")
		}
	}
	return envelope.Data.Results
}

func batchOutcome(t *testing.T, result commercialBatchResult, want string) {
	t.Helper()
	if result.Receipt["outcome"] != want || result.Error.Code != "" {
		t.Fatalf("result %+v, want receipt %s", result, want)
	}
}

func TestCommercialBatchAtomicAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, boundary := range []string{"effect", "audit", "receipt", "commit"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				f := newBatchCommercialFixture(t, kind)
				ctx := context.Background()
				state := f.create(t)
				before := f.read(t, state.ID)
				op := f.operation(state, "edit", map[string]any{"notes": "Once after retry"})
				id := op["operation_id"].(string)
				table, event, timing := kind+"s", "UPDATE", "BEFORE"
				if boundary == "audit" {
					table, event = "authorization_audit", "INSERT"
				}
				if boundary == "receipt" || boundary == "commit" {
					table, event = "commercial_operations", "INSERT"
				}
				trigger := "CREATE TRIGGER"
				if boundary == "commit" {
					trigger, timing = "CREATE CONSTRAINT TRIGGER", "AFTER"
				}
				deferred := ""
				if boundary == "commit" {
					deferred = " DEFERRABLE INITIALLY DEFERRED"
				}
				_, err := f.pool.Exec(ctx, `CREATE FUNCTION batch_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected batch storage failure' USING ERRCODE='23514'; END $$; `+trigger+` batch_failure `+timing+` `+event+` ON `+table+deferred+` FOR EACH ROW EXECUTE FUNCTION batch_failure()`)
				historyCheck(t, err)
				result := batchResults(t, f, op)[0]
				if result.Error.Code != "retryable_failure" || result.Receipt != nil {
					t.Errorf("rollback exposed terminal receipt: %+v", result)
				}
				lifecycleCall(t, f.api, f.token, "GET", "/commercial-operations/"+id, "", 404)
				if f.read(t, state.ID) != before {
					t.Fatal("rollback changed content or revision")
				}
				var count int
				historyCheck(t, f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$3)+(SELECT count(*) FROM commercial_operations WHERE operation_id=$3)`, kind+".edit", state.ID, id).Scan(&count))
				if count != 0 {
					t.Fatal("rollback retained audit/admission/receipt")
				}
				_, err = f.pool.Exec(ctx, `DROP TRIGGER batch_failure ON `+table)
				historyCheck(t, err)
				accepted := batchResults(t, f, op)[0]
				batchOutcome(t, accepted, "accepted")
				batchOutcome(t, batchResults(t, f, op)[0], "accepted")
				assertContains(t, f.read(t, state.ID), `"notes":"Once after retry"`)
				historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2`, kind+".edit", state.ID).Scan(&count))
				if count != 1 {
					t.Fatalf("retry/replay wrote %d activity entries", count)
				}
			})
		}
	}
}
