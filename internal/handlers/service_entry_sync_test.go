package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func serviceEntrySchemaValidator(t *testing.T, api http.Handler) func(string, []byte) {
	t.Helper()
	document := serviceAPIRequest(api, "", "GET", "/openapi.json", nil)
	var spec map[string]any
	historyCheck(t, json.Unmarshal(document.Body.Bytes(), &spec))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	historyCheck(t, compiler.AddResource("https://service-entry.test/openapi.json", spec))
	return func(name string, raw []byte) {
		t.Helper()
		schema, err := compiler.Compile("https://service-entry.test/openapi.json#/components/schemas/" + name)
		historyCheck(t, err)
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		historyCheck(t, err)
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, raw)
		}
	}
}

func TestServiceEntrySyncSnapshotDeltaAndOpaqueTargets(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Sync Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Service sync").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Service sync")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	validate := serviceEntrySchemaValidator(t, api)
	readSync := func(path string) map[string]any {
		t.Helper()
		r := serviceAPIRequest(api, token, "GET", path, nil)
		if r.Code != 200 {
			t.Fatalf("sync %d %s", r.Code, r.Body.String())
		}
		validate("JobSyncEnvelope", r.Body.Bytes())
		var envelope map[string]any
		historyCheck(t, json.Unmarshal(r.Body.Bytes(), &envelope))
		return envelope["data"].(map[string]any)
	}
	page := readSync("/sync/jobs")
	job := page["records"].([]any)[0].(map[string]any)
	asset := job["masters"].(map[string]any)["assets"].([]any)[0].(map[string]any)
	capability, ok := asset["service_entry_context"].(map[string]any)
	if !ok || capability["can_create"] != true {
		t.Fatalf("missing service-entry prerequisites/capabilities: %#v", asset)
	}
	path := fmt.Sprintf("/jobs/%s/assets/%s/service-entries", job["id"], asset["id"])
	unknownFilter := serviceAPIRequest(api, token, "GET", path+"?include=association_history", nil)
	if unknownFilter.Code != 400 {
		t.Fatalf("unknown Service Entry filter: %d %s", unknownFilter.Code, unknownFilter.Body.String())
	}
	validate("ErrorEnvelope", unknownFilter.Body.Bytes())
	intent := map[string]any{"operation_id": newOperationKey(), "link_id": capability["link_id"], "service_date": time.Now().UTC().Format("2006-01-02"), "summary": "Synchronized original", "responsible_user_ids": []int64{admin}}
	created := serviceAPIRequest(api, token, "POST", path, intent)
	if created.Code != 200 {
		t.Fatalf("opaque create: %d %s", created.Code, created.Body.String())
	}
	validate("ServiceEntryReceiptEnvelope", created.Body.Bytes())
	raw, err := json.Marshal(intent)
	historyCheck(t, err)
	validate("ServiceEntryCreate", raw)
	delta := readSync("/sync/jobs?cursor=" + url.QueryEscape(page["completion_cursor"].(string)))
	updated := delta["records"].([]any)[0].(map[string]any)
	updatedAsset := updated["masters"].(map[string]any)["assets"].([]any)[0].(map[string]any)
	entry := updatedAsset["service_entries"].([]any)[0].(map[string]any)
	if entry["asset_id"] != asset["id"] || entry["job_id"] != job["id"] || entry["can_revise"] != true || entry["summary"] != "Synchronized original" {
		t.Fatalf("incomplete sync entry: %#v", entry)
	}
	intent["operation_id"], intent["revision"], intent["summary"] = newOperationKey(), entry["revision"], "Reviewed synchronized revision"
	revised := serviceAPIRequest(api, token, "POST", path+"/"+entry["id"].(string), intent)
	if revised.Code != 200 {
		t.Fatalf("opaque revision: %d %s", revised.Code, revised.Body.String())
	}
	validate("ServiceEntryReceiptEnvelope", revised.Body.Bytes())
	raw, err = json.Marshal(intent)
	historyCheck(t, err)
	validate("ServiceEntryRevise", raw)
	last := readSync("/sync/jobs?cursor=" + url.QueryEscape(delta["completion_cursor"].(string)))
	lastJob := last["records"].([]any)[0].(map[string]any)
	lastEntry := lastJob["masters"].(map[string]any)["assets"].([]any)[0].(map[string]any)["service_entries"].([]any)[0].(map[string]any)
	if lastEntry["id"] != entry["id"] || lastEntry["revision"] == entry["revision"] || lastEntry["summary"] != "Reviewed synchronized revision" {
		t.Fatalf("delta identity/revision: %#v", lastEntry)
	}
	// Batch dependencies preserve the originally read revision. A failed root
	// blocks descendants while an unrelated create is independently accepted.
	stale, blocked, independent := newOperationKey(), newOperationKey(), newOperationKey()
	member := func(id, kind, revision string, deps []string) map[string]any {
		v := map[string]any{"operation_id": id, "type": kind, "job_id": job["id"], "asset_id": asset["id"], "depends_on": deps, "link_id": capability["link_id"], "service_date": intent["service_date"], "summary": "Batch service", "responsible_user_ids": []int64{admin}}
		if kind == "service_entry.revise" {
			v["entry_id"], v["revision"] = entry["id"], revision
		}
		return v
	}
	ops := []any{member(stale, "service_entry.revise", entry["revision"].(string), []string{}), member(blocked, "service_entry.create", "", []string{stale}), member(independent, "service_entry.create", "", []string{})}
	batch := serviceAPIRequest(api, token, "POST", "/sync/operations", map[string]any{"operations": ops})
	if batch.Code != 200 {
		t.Fatalf("batch: %d %s", batch.Code, batch.Body.String())
	}
	validate("OperationBatchEnvelope", batch.Body.Bytes())
	raw, err = json.Marshal(map[string]any{"operations": ops})
	historyCheck(t, err)
	validate("OperationBatch", raw)
	var result struct {
		Data struct {
			Results []struct {
				Receipt struct {
					Code string `json:"code"`
				} `json:"receipt"`
			} `json:"results"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal(batch.Body.Bytes(), &result))
	for i, want := range []string{"service_entry_conflict", "failed_dependency", "accepted"} {
		if result.Data.Results[i].Receipt.Code != want {
			t.Fatalf("batch result: %s", batch.Body.String())
		}
	}
	// Replaying the exact batch cannot duplicate the unrelated accepted effect.
	if replay := serviceAPIRequest(api, token, "POST", "/sync/operations", map[string]any{"operations": ops}); replay.Code != 200 {
		t.Fatalf("batch replay: %d %s", replay.Code, replay.Body.String())
	}
	repair := newOperationKey()
	repaired := serviceAPIRequest(api, token, "POST", "/sync/operations", map[string]any{"operations": []any{member(repair, "service_entry.revise", lastEntry["revision"].(string), []string{}), member(newOperationKey(), "service_entry.create", "", []string{repair})}})
	if repaired.Code != 200 {
		t.Fatalf("repair: %d %s", repaired.Code, repaired.Body.String())
	}
	historyCheck(t, json.Unmarshal(repaired.Body.Bytes(), &result))
	for _, r := range result.Data.Results {
		if r.Receipt.Code != "accepted" {
			t.Fatalf("reviewed repair: %s", repaired.Body.String())
		}
	}
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries WHERE job_id=$1`, j.ID).Scan(&count))
	if count != 3 {
		t.Fatalf("batch effects count=%d", count)
	}
}

// Broad office grants must never turn a lost field assignment into authority.
func TestServiceEntryAPIAssignmentLossWithBroadGrants(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Assigned service").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Service assignment loss")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	read := serviceAPIRequest(api, token, "GET", path, nil)
	if read.Code != 200 {
		t.Fatalf("read: %d %s", read.Code, read.Body.String())
	}
	var page struct {
		Data struct {
			LinkID int64 `json:"link_id"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal(read.Body.Bytes(), &page))
	intent := map[string]any{"operation_id": newOperationKey(), "link_id": page.Data.LinkID, "service_date": time.Now().UTC().Format("2006-01-02"), "summary": "Original intent", "responsible_user_ids": []int64{admin}}
	created := serviceAPIRequest(api, token, "POST", path, intent)
	if created.Code != 200 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var receipt struct {
		Data struct {
			EntryID int64 `json:"entry_id"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal(created.Body.Bytes(), &receipt))
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, admin)
	historyCheck(t, err)
	// Original accepted replay stays queryable, with no sensitive representation.
	replayed := serviceAPIRequest(api, token, "POST", path, intent)
	if replayed.Code != 200 {
		t.Fatalf("replay: %d %s", replayed.Code, replayed.Body.String())
	}
	for _, endpoint := range []string{path, fmt.Sprintf("%s/%d/revisions", path, receipt.Data.EntryID)} {
		response := serviceAPIRequest(api, token, "GET", endpoint, nil)
		if response.Code != 404 {
			t.Fatalf("lost assignment read %s: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
	intent["operation_id"] = newOperationKey()
	response := serviceAPIRequest(api, token, "POST", path, intent)
	if response.Code != 404 {
		t.Fatalf("lost assignment create: %d %s", response.Code, response.Body.String())
	}
	var entries int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries WHERE job_id=$1`, j.ID).Scan(&entries))
	if entries != 1 {
		t.Fatalf("unauthorized effect: %d entries", entries)
	}
}
