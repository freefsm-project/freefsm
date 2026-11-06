package handlers

import (
	"context"
	"fmt"
	"testing"
)

func TestCommercialBatchDependenciesAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			f := newBatchCommercialFixture(t, kind)
			a, b, c := f.create(t), f.create(t), f.create(t)
			first := f.operation(a, "edit", map[string]any{"notes": "First"})
			second := f.operation(b, "edit", map[string]any{"notes": "Second"}, first["operation_id"].(string))
			for _, r := range batchResults(t, f, first, second) {
				batchOutcome(t, r, "accepted")
			}
			assertContains(t, f.read(t, a.ID), `"notes":"First"`)
			assertContains(t, f.read(t, b.ID), `"notes":"Second"`)
			known := f.operation(c, "edit", map[string]any{"notes": "Known receipts"}, first["operation_id"].(string), second["operation_id"].(string))
			batchOutcome(t, batchResults(t, f, known)[0], "accepted")
			// Dependencies are ordered immutable intent, not a normalized set.
			for _, deps := range [][]string{{second["operation_id"].(string), first["operation_id"].(string)}, {first["operation_id"].(string), second["operation_id"].(string), second["operation_id"].(string)}} {
				changed := map[string]any{}
				for k, v := range known {
					changed[k] = v
				}
				changed["depends_on"] = deps
				r := batchResults(t, f, changed)[0]
				if r.Error.Code != "operation_intent_changed" || r.Receipt != nil {
					t.Fatalf("dependency rewrite: %+v", r)
				}
			}
			duplicate := f.operation(matrixState(t, f.read(t, c.ID)), "edit", map[string]any{"terms": "Duplicate references retained"}, first["operation_id"].(string), first["operation_id"].(string))
			batchOutcome(t, batchResults(t, f, duplicate)[0], "accepted")
			batchOutcome(t, batchResults(t, f, duplicate)[0], "accepted")
			for _, scenario := range []string{"unknown", "self", "forward", "cycle", "stale"} {
				t.Run(scenario, func(t *testing.T) {
					x := f.operation(a, "edit", map[string]any{"notes": "Must remain First"})
					y := f.operation(b, "edit", map[string]any{"notes": "Must remain Second"}, x["operation_id"].(string))
					want := "invalid_dependency"
					switch scenario {
					case "unknown":
						x["depends_on"] = []string{newOperationKey()}
					case "self":
						x["depends_on"] = []string{x["operation_id"].(string)}
					case "forward", "cycle":
						x["depends_on"] = []string{y["operation_id"].(string)}
					case "stale":
						x["depends_on"], want = []string{first["operation_id"].(string)}, "stale_commercial_revision"
					}
					if scenario == "forward" {
						y["depends_on"] = []string{}
					}
					unrelated := f.operation(matrixState(t, f.read(t, c.ID)), "edit", map[string]any{"notes": "Unrelated " + scenario})
					results := batchResults(t, f, x, y, unrelated)
					batchOutcome(t, results[0], want)
					if scenario == "forward" {
						batchOutcome(t, results[1], "stale_commercial_revision")
					} else {
						batchOutcome(t, results[1], "failed_dependency")
					}
					batchOutcome(t, results[2], "accepted")
					// Historical terminal replay precedes dependency/order revalidation.
					batchOutcome(t, batchResults(t, f, x)[0], want)
					assertContains(t, f.read(t, a.ID), `"notes":"First"`)
					assertContains(t, f.read(t, b.ID), `"notes":"Second"`)
				})
			}
		})
	}
}

func TestCommercialBatchLifecycleAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			f := newBatchCommercialFixture(t, kind)
			state := f.create(t)
			var field int64
			historyCheck(t, f.pool.QueryRow(context.Background(), `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,$2,'Batch field','text') RETURNING id`, f.company, kind).Scan(&field))
			op := f.operation(state, "edit", map[string]any{"notes": "Field edit", "custom_fields": []map[string]any{{"definition_id": field, "value": "Changed offline"}}})
			batchOutcome(t, batchResults(t, f, op)[0], "accepted")
			assertContains(t, f.read(t, state.ID), "Changed offline")
			categories := []string{"estimate:accepted", "estimate:sent", "estimate:draft"}
			if kind == "invoice" {
				categories = []string{"invoice:invoiced", "invoice:sent", "invoice:void", "invoice:invoiced", "invoice:draft"}
			}
			for i, category := range categories {
				current := matrixState(t, f.read(t, state.ID))
				for _, choice := range current.Workflow {
					if choice.Category != category {
						continue
					}
					action := "status"
					if kind == "invoice" && i == 0 {
						action = "finalize"
					}
					command := f.operation(current, action, map[string]any{"status_id": choice.ID, "meaning_revision": choice.Meaning}, op["operation_id"].(string))
					batchOutcome(t, batchResults(t, f, command)[0], "accepted")
					batchOutcome(t, batchResults(t, f, command)[0], "accepted")
					after := matrixState(t, f.read(t, state.ID))
					if after.Manual != category {
						t.Fatalf("workflow target changed: %+v", after)
					}
					if kind == "invoice" && category == "invoice:invoiced" && after.Effective != "invoice:paid" {
						t.Fatal("zero total did not project Paid")
					}
					if action == "status" && category != "invoice:draft" {
						var beforeAudit, afterAudit int
						historyCheck(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM authorization_audit WHERE target_type=$1 AND target_id=$2`, kind, state.ID).Scan(&beforeAudit))
						noop := f.operation(after, "status", map[string]any{"status_id": choice.ID, "meaning_revision": choice.Meaning})
						batchOutcome(t, batchResults(t, f, noop)[0], "accepted")
						historyCheck(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM authorization_audit WHERE target_type=$1 AND target_id=$2`, kind, state.ID).Scan(&afterAudit))
						if afterAudit != beforeAudit || matrixState(t, f.read(t, state.ID)).Revision != after.Revision {
							t.Fatal("desired-state no-op rewrote attribution/revision")
						}
					}
					// A new ID cannot make the originally reviewed meaning current.
					wrong := f.operation(after, "status", map[string]any{"status_id": choice.ID, "meaning_revision": newOperationKey()})
					batchOutcome(t, batchResults(t, f, wrong)[0], "commercial_conflict")
				}
			}
			var count int
			historyCheck(t, f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM payment_invoice_allocations)+(SELECT count(*) FROM credit_applications)`).Scan(&count))
			if count != 0 {
				t.Fatal("workflow fabricated financial history")
			}
		})
	}
}

func TestCommercialBatchTransportAndAuthorityAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			f := newBatchCommercialFixture(t, kind)
			state := f.create(t)
			path := fmt.Sprintf("/%ss/%d", kind, state.ID)
			op := f.operation(state, "edit", map[string]any{"notes": "Original batch"})
			batchOutcome(t, batchResults(t, f, op)[0], "accepted")
			for _, change := range []string{"target", "revision", "notes"} {
				changed := f.operation(state, "edit", map[string]any{"notes": "Original batch"})
				changed["operation_id"] = op["operation_id"]
				switch change {
				case "target":
					changed["target"] = fmt.Sprint(state.ID + 1000)
				case "revision":
					changed["commercial"].(map[string]any)["revision"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				case "notes":
					changed["commercial"].(map[string]any)["notes"] = "Changed intent"
				}
				r := batchResults(t, f, changed)[0]
				if r.Error.Code != "operation_intent_changed" || r.Receipt != nil {
					t.Fatalf("same-ID %s changed: %+v", change, r)
				}
			}
			standalone := map[string]any{"operation_id": op["operation_id"], "job_id": f.job, "revision": state.Revision, "notes": "Original batch"}
			other := f
			other.token = f.otherToken
			if batchResults(t, other, op)[0].Error.Code != "operation_intent_changed" {
				t.Fatal("cross-client replay rebound identity")
			}
			crossClient := f.operation(state, "edit", map[string]any{"notes": "Cross client"}, op["operation_id"].(string))
			batchOutcome(t, batchResults(t, other, crossClient)[0], "invalid_dependency")
			remote := newBatchCommercialFixture(t, kind)
			remoteState := remote.create(t)
			crossDeployment := remote.operation(remoteState, "edit", map[string]any{"notes": "Cross deployment"}, op["operation_id"].(string))
			batchOutcome(t, batchResults(t, remote, crossDeployment)[0], "invalid_dependency")
			lifecycleCall(t, f.api, f.token, "PATCH", path, matrixBody(t, standalone), 409)
			current := matrixState(t, f.read(t, state.ID))
			single := f.operation(current, "edit", map[string]any{"notes": "Standalone"})
			singleBody := map[string]any{"operation_id": single["operation_id"], "job_id": f.job, "revision": current.Revision, "notes": "Standalone"}
			original := lifecycleCall(t, f.api, f.token, "PATCH", path, matrixBody(t, singleBody), 200)
			if batchResults(t, f, single)[0].Error.Code != "operation_intent_changed" {
				t.Fatal("standalone identity silently migrated to batch")
			}
			if lifecycleCall(t, f.api, f.token, "PATCH", path, matrixBody(t, singleBody), 200) != original {
				t.Fatal("standalone receipt changed")
			}
			// A Job-bounded request never rebinds its Customer or Job.
			wrong := f.operation(matrixState(t, f.read(t, state.ID)), "edit", map[string]any{"customer_id": f.customer + 1000, "notes": "Retarget"})
			batchOutcome(t, batchResults(t, f, wrong)[0], "commercial_conflict")
			wrong = f.operation(matrixState(t, f.read(t, state.ID)), "edit", map[string]any{"notes": "Retarget Job"})
			wrong["commercial"].(map[string]any)["job_id"] = f.job + 1000
			batchOutcome(t, batchResults(t, f, wrong)[0], "commercial_conflict")
			// Archive is a current domain boundary even for the protected actor.
			if kind == "invoice" {
				matrixTransition(t, f.api, f.token, path, f.job, matrixState(t, f.read(t, state.ID)), "invoice:invoiced", "finalize", 200)
			}
			_, err := f.pool.Exec(context.Background(), "UPDATE "+kind+"s SET deleted_at=now() WHERE id=$1", state.ID)
			historyCheck(t, err)
			batchOutcome(t, batchResults(t, f, op)[0], "accepted")
			fresh := f.operation(current, "edit", map[string]any{"notes": "After archive"})
			result := batchResults(t, f, fresh)[0]
			if result.Receipt["outcome"] == "accepted" || result.Receipt == nil {
				t.Fatalf("archived new effect %+v", result)
			}
			lifecycleCall(t, f.api, f.token, "GET", "/commercial-operations/"+op["operation_id"].(string), "", 200)
		})
	}
}
