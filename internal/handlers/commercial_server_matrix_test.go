package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

type commercialMatrixState struct {
	ID        int64  `json:"id"`
	Revision  string `json:"revision"`
	Manual    string `json:"manual_category"`
	Effective string `json:"effective_category"`
	Locked    bool   `json:"settlement_locked"`
	Workflow  []struct {
		ID       int64  `json:"id"`
		Category string `json:"category"`
		Meaning  string `json:"meaning_revision"`
	} `json:"workflow"`
}

func matrixState(t *testing.T, raw string) commercialMatrixState {
	t.Helper()
	var envelope struct {
		Data commercialMatrixState `json:"data"`
	}
	historyCheck(t, json.Unmarshal([]byte(raw), &envelope))
	return envelope.Data
}

func matrixBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	historyCheck(t, err)
	return string(raw)
}

func matrixTransition(t *testing.T, api http.Handler, token, path string, job int64, state commercialMatrixState, category, action string, want int) string {
	t.Helper()
	for _, choice := range state.Workflow {
		if choice.Category == category {
			return lifecycleCall(t, api, token, "POST", path+"/"+action, matrixBody(t, map[string]any{
				"operation_id": newOperationKey(), "job_id": job, "revision": state.Revision,
				"status_id": choice.ID, "meaning_revision": choice.Meaning,
			}), want)
		}
	}
	t.Fatalf("missing workflow category %s", category)
	return ""
}

func TestCommercialServerWholeRevisionAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revision customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Revision job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			token, err := sessions.CreateMobile(ctx, admin, "Whole revision")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			created := matrixState(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Original"}), 200))
			path := fmt.Sprintf("/%ss/%d", kind, created.ID)
			read := func() commercialMatrixState {
				return matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
			}
			edit := func(revision string, fields map[string]any, want int) string {
				fields["operation_id"], fields["job_id"], fields["revision"] = newOperationKey(), j.ID, revision
				return lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, fields), want)
			}
			original := read()
			edit(original.Revision, map[string]any{"notes": "Office edit"}, 200)
			assertContains(t, edit(original.Revision, map[string]any{"terms": "Disjoint offline edit"}, 409), `"stale_commercial_revision"`)
			edit(read().Revision, map[string]any{"notes": ""}, 200)
			if read().Revision == original.Revision {
				t.Fatal("commercial ABA reused revision")
			}
			assertContains(t, edit(original.Revision, map[string]any{"title": "Stale ABA"}, 409), `"stale_commercial_revision"`)
			var field int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,$2,'Reviewed value','text') RETURNING id`, company, kind).Scan(&field))
			reviewed := read()
			edit(reviewed.Revision, map[string]any{"custom_fields": []map[string]any{{"definition_id": field, "value": "Field edit"}}}, 200)
			assertContains(t, edit(reviewed.Revision, map[string]any{"terms": "Stale across fields"}, 409), `"stale_commercial_revision"`)
			if kind == "invoice" {
				assertContains(t, matrixTransition(t, api, token, path, j.ID, reviewed, "invoice:invoiced", "finalize", 409), `"stale_commercial_revision"`)
			} else {
				assertContains(t, matrixTransition(t, api, token, path, j.ID, reviewed, "estimate:accepted", "status", 409), `"stale_commercial_revision"`)
			}
			// Real collaboration writes must not invalidate the original commercial review.
			reviewed = read()
			cookie := sessionCookie(t, ctx, sessions, admin)
			documentTransactionRequest(t, web, cookie, "POST", path+"/comments", url.Values{"content": {"Independent collaboration"}}, 200)
			commercialUpload(t, web, cookie, kind, created.ID)
			if read().Revision != reviewed.Revision {
				t.Fatal("Comment/File invalidated commercial review")
			}
			edit(reviewed.Revision, map[string]any{"terms": "Reviewed after collaboration"}, 200)
		})
	}
}

func TestCommercialServerWorkflowMatrixAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Workflow customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Workflow job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Workflow matrix")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	create := func(kind string) string {
		state := matrixState(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Workflow matrix", "line_items": []any{}}), 200))
		return fmt.Sprintf("/%ss/%d", kind, state.ID)
	}
	t.Run("configured estimates", func(t *testing.T) {
		path := create("estimate")
		svc := statusflow.New(pool)
		for _, category := range []string{"estimate:draft", "estimate:estimate", "estimate:sent", "estimate:accepted", "estimate:rejected", "estimate:completed"} {
			t.Run(category, func(t *testing.T) {
				choice, err := svc.Create(ctx, statusflow.Actor{ID: admin, CompanyID: company}, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Configured " + category, Category: statusflow.CategoryKey(category)})
				historyCheck(t, err)
				state := matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
				var meaning string
				for _, v := range state.Workflow {
					if v.ID == choice.ID {
						meaning = v.Meaning
					}
				}
				lifecycleCall(t, api, token, "POST", path+"/status", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state.Revision, "status_id": choice.ID, "meaning_revision": meaning}), 200)
				state = matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
				lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state.Revision, "notes": "Editable in " + category}), 200)
				if after := matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200)); after.Manual != category {
					t.Fatalf("edit changed manual status: %+v", after)
				}
			})
		}
	})
	t.Run("zero invoice manual transitions", func(t *testing.T) {
		path := create("invoice")
		read := func() commercialMatrixState {
			return matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
		}
		for _, target := range []string{"invoice:invoiced", "invoice:sent", "invoice:void"} {
			matrixTransition(t, api, token, path, j.ID, read(), target, "status", 409)
		}
		for _, from := range []string{"invoice:invoiced", "invoice:sent", "invoice:void"} {
			for _, to := range []string{"invoice:invoiced", "invoice:sent", "invoice:void", "invoice:draft"} {
				t.Run(from+" to "+to, func(t *testing.T) {
					if read().Manual == "invoice:draft" {
						matrixTransition(t, api, token, path, j.ID, read(), "invoice:invoiced", "finalize", 200)
					}
					matrixTransition(t, api, token, path, j.ID, read(), from, "status", 200)
					matrixTransition(t, api, token, path, j.ID, read(), to, "status", 200)
					state := read()
					want := "invoice:paid"
					if to == "invoice:draft" || to == "invoice:void" {
						want = to
					}
					if state.Manual != to || state.Effective != want || state.Locked {
						t.Fatalf("wrong zero-total projection: %+v", state)
					}
				})
			}
		}
		for _, category := range []string{"invoice:paid", "invoice:partially_paid"} {
			var id int64
			var meaning string
			historyCheck(t, pool.QueryRow(ctx, `SELECT id,meaning_revision::text FROM statuses WHERE company_id=$1 AND category_key=$2`, company, category).Scan(&id, &meaning))
			lifecycleCall(t, api, token, "POST", path+"/status", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": read().Revision, "status_id": id, "meaning_revision": meaning}), 422)
		}
		var financial int
		historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM payment_invoice_allocations)+(SELECT count(*) FROM credit_applications)`).Scan(&financial))
		if financial != 0 {
			t.Fatal("workflow fabricated financial history")
		}
	})
}

func TestCommercialServerWorkflowReplacementAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Meaning customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Meaning job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Meaning replacement")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	created := matrixState(t, lifecycleCall(t, api, token, "POST", "/estimates", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Meaning"}), 200))
	path := fmt.Sprintf("/estimates/%d", created.ID)
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	choice, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Reviewed choice", Category: statusflow.EstimateAccepted})
	historyCheck(t, err)
	read := func() commercialMatrixState {
		return matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
	}
	state := read()
	var meaning string
	for _, v := range state.Workflow {
		if v.ID == choice.ID {
			meaning = v.Meaning
		}
	}
	op := newOperationKey()
	body := matrixBody(t, map[string]any{"operation_id": op, "job_id": j.ID, "revision": state.Revision, "status_id": choice.ID, "meaning_revision": meaning})
	// Changing an unselected target's meaning leaves this document untouched;
	// rejection must therefore depend on the reviewed meaning, not content drift.
	historyCheck(t, svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: choice.ID, Category: statusflow.EstimateRejected}))
	if read().Revision != state.Revision {
		t.Fatal("unselected workflow choice changed document revision")
	}
	assertContains(t, lifecycleCall(t, api, token, "POST", path+"/status", body, 409), `"commercial_conflict"`)
	historyCheck(t, svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: choice.ID, Category: statusflow.EstimateAccepted}))
	body = matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state.Revision, "status_id": choice.ID, "meaning_revision": meaning})
	assertContains(t, lifecycleCall(t, api, token, "POST", path+"/status", body, 409), `"commercial_conflict"`)
	for _, v := range read().Workflow {
		if v.ID == choice.ID {
			meaning = v.Meaning
		}
	}
	op = newOperationKey()
	body = matrixBody(t, map[string]any{"operation_id": op, "job_id": j.ID, "revision": state.Revision, "status_id": choice.ID, "meaning_revision": meaning})
	replacement, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Replacement choice", Category: statusflow.EstimateAccepted})
	historyCheck(t, err)
	historyCheck(t, svc.Delete(ctx, actor, choice.ID, replacement.ID))
	_, err = svc.Update(ctx, actor, replacement.ID, "Reviewed choice", "#123456")
	historyCheck(t, err)
	assertContains(t, lifecycleCall(t, api, token, "POST", path+"/status", body, 409), `"commercial_conflict"`)
	state = read()
	for _, v := range state.Workflow {
		if v.ID == replacement.ID {
			meaning = v.Meaning
		}
	}
	changed := map[string]any{"operation_id": op, "job_id": j.ID, "revision": state.Revision, "status_id": replacement.ID, "meaning_revision": meaning}
	assertContains(t, lifecycleCall(t, api, token, "POST", path+"/status", matrixBody(t, changed), 409), `"operation_intent_changed"`)
	changed["operation_id"] = newOperationKey()
	lifecycleCall(t, api, token, "POST", path+"/status", matrixBody(t, changed), 200)
	state = read()
	var before int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='estimate' AND object_id=$1 AND action='status_transitioned'`, created.ID).Scan(&before))
	changed["operation_id"], changed["revision"] = newOperationKey(), state.Revision
	lifecycleCall(t, api, token, "POST", path+"/status", matrixBody(t, changed), 200)
	var after int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='estimate' AND object_id=$1 AND action='status_transitioned'`, created.ID).Scan(&after))
	if after != before || read().Revision != state.Revision {
		t.Fatal("desired-state no-op rewrote status attribution/revision")
	}
}
