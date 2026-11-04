package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestFieldLeaseIndicatorServiceHistoryHTTP(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Indicator Customer").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Private premises").SaveX(ctx)
	typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Indicator equipment").SaveX(ctx)
	a := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Operational equipment").SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Authorized service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, f.sessions, f.admin)
	var today time.Time
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
	date := today.Format(time.DateOnly)
	fieldPost(t, f.router, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var lease int64
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&lease))
	path := fmt.Sprintf("/leases/%d", lease)
	fieldPost(t, f.router, cookie, path, url.Values{"revision": {"0"}, "title": {"PRIVATE-LEASE"}, "starts_on": {date}, "ends_on": {today.AddDate(0, 1, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	initial, rent := int64(0), int64(5000)
	svc := authorization.New(f.pool)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: f.admin}, lease, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &initial, RentCents: &rent, TaxRate: "0"}, 0))
	fieldPost(t, f.router, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {date}}, 303)
	entriesPath := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	serviceEntryPost(t, f.router, cookie, entriesPath, url.Values{"service_date": {date}, "summary": {"Authorized row fact"}, "responsible_user_id": {fmt.Sprint(f.admin)}}, 303)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}}
	uid, role, _ := f.reader(t, grants)
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	token, err := f.sessions.CreateMobile(ctx, uid, "Indicator reference")
	historyCheck(t, err)
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	validate := fieldLeaseSchema(t)
	get := func(path string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		assertNotContains(t, w.Body.String(), "PRIVATE-LEASE")
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		return envelope.Data
	}
	check := func(row map[string]any) {
		t.Helper()
		coverage, ok := row["lease_coverage"].(map[string]any)
		if !ok || len(coverage) != 2 || coverage["covered"] != true {
			t.Fatalf("bounded current coverage: %v", row)
		}
		_, err := time.Parse(time.RFC3339Nano, coverage["as_of"].(string))
		historyCheck(t, err)
	}
	masterPath := fmt.Sprintf("/jobs/%d/master-context", j.ID)
	check(get(masterPath, 200)["asset"].(map[string]any))
	check(get(entriesPath, 200)["entries"].([]any)[0].(map[string]any))
	// Use the actual synchronized key, never a numeric stand-in for opaque IDs.
	snapshot := get("/sync/jobs", 200)
	validate("JobSyncEnvelope", map[string]any{"data": snapshot})
	checkSync := func(data map[string]any, count int) string {
		t.Helper()
		records := data["records"].([]any)
		if len(records) != 1 {
			t.Fatalf("populated sync records: %v", records)
		}
		masters := records[0].(map[string]any)["masters"].(map[string]any)
		asset := masters["asset"].(map[string]any)
		validate("SyncAsset", asset)
		entries := asset["service_entries"].([]any)
		if len(entries) != count {
			t.Fatalf("populated sync entries: %v", entries)
		}
		var first string
		for _, value := range entries {
			entry := value.(map[string]any)
			validate("SyncServiceEntry", entry)
			validate("FieldLeaseCoverage", entry["lease_coverage"])
			check(entry)
			id, ok := entry["id"].(string)
			if !ok || id == "" {
				t.Fatalf("sync row identity is not opaque: %v", entry)
			}
			if entry["summary"] == "Authorized row fact" {
				first = id
			}
		}
		if first == "" {
			t.Fatal("original synchronized row missing")
		}
		return first
	}
	firstRowID := checkSync(snapshot, 1)
	serviceEntryPost(t, f.router, cookie, entriesPath, url.Values{"service_date": {date}, "summary": {"Delta row fact"}, "responsible_user_id": {fmt.Sprint(f.admin)}}, 303)
	delta := get("/sync/jobs?cursor="+url.QueryEscape(snapshot["completion_cursor"].(string)), 200)
	validate("JobSyncEnvelope", map[string]any{"data": delta})
	if checkSync(delta, 2) != firstRowID {
		t.Fatal("synchronized row identity changed across delta")
	}
	synced := snapshot["records"].([]any)
	if len(synced) != 1 {
		t.Fatalf("assigned sync: %v", synced)
	}
	opaque := synced[0].(map[string]any)["id"].(string)
	masterPath = "/jobs/" + opaque + "/master-context"
	entriesPath = fmt.Sprintf("/jobs/%s/assets/%d/service-entries", opaque, a.ID)
	asset := get(masterPath, 200)["asset"].(map[string]any)
	validate("FieldLeaseAsset", asset)
	check(asset)
	row := get(entriesPath, 200)["entries"].([]any)[0].(map[string]any)
	validate("FieldLeaseServiceEntry", row)
	check(row)
	revisions := serviceAPIRequest(api, token, "GET", fmt.Sprintf("%s/%.0f/revisions", entriesPath, row["id"]), nil)
	if revisions.Code != 200 {
		t.Fatalf("revisions: %d %s", revisions.Code, revisions.Body.String())
	}
	var revisionEnvelope struct {
		Data []map[string]any `json:"data"`
	}
	historyCheck(t, json.Unmarshal(revisions.Body.Bytes(), &revisionEnvelope))
	if len(revisionEnvelope.Data) != 1 {
		t.Fatal("missing authorized revision")
	}
	validate("FieldServiceEntryRevision", revisionEnvelope.Data[0])
	// Identity translation outages remain retryable, not inaccessible targets.
	_, err = f.pool.Exec(ctx, `ALTER TABLE job_sync_identities RENAME TO indicator_identity_unavailable`)
	historyCheck(t, err)
	for _, suffix := range []string{"/master-context", "/customer-context", fmt.Sprintf("/assets/%d/service-entries", a.ID)} {
		get("/jobs/"+opaque+suffix, 503)
	}
	_, err = f.pool.Exec(ctx, `ALTER TABLE indicator_identity_unavailable RENAME TO job_sync_identities`)
	historyCheck(t, err)
	for _, key := range []string{"malformed", "00000000-0000-4000-8000-000000000000", "0", "-1"} {
		get("/jobs/"+key+"/master-context", 404)
		get(fmt.Sprintf("/jobs/%s/assets/%d/service-entries", key, a.ID), 404)
	}
	// Even explicit commercial grants cannot expand the field projection.
	grants = append(grants, authorization.Grant{Permission: "lease.view", Scope: authorization.All}, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", grants, 1)
	historyCheck(t, err)
	check(get(entriesPath, 200)["entries"].([]any)[0].(map[string]any))
	validate("FieldLeaseAsset", get(masterPath, 200)["asset"])
	validate("FieldLeaseServiceEntry", get(entriesPath, 200)["entries"].([]any)[0])
	get(path, 404)
	// Reads are observations, not lifecycle workers or history writers.
	tables := []string{"leases", "lease_asset_dependencies", "lease_billing_obligations", "invoices", "authorization_audit", "asset_service_revisions"}
	before := historyDurableRows(t, f.pool, tables)
	for range 2 {
		check(get(masterPath, 200)["asset"].(map[string]any))
		check(get(entriesPath, 200)["entries"].([]any)[0].(map[string]any))
	}
	historyEqual(t, "read-only indicator", historyDurableRows(t, f.pool, tables), before)
	// A domain read failure must not become either an uncovered value or an
	// authority-loss response that causes the client to mistake an outage for loss.
	_, err = f.pool.Exec(ctx, `ALTER TABLE lease_asset_dependencies RENAME TO indicator_unavailable`)
	historyCheck(t, err)
	get(masterPath, 503)
	get(entriesPath, 503)
	_, err = f.pool.Exec(ctx, `ALTER TABLE indicator_unavailable RENAME TO lease_asset_dependencies`)
	historyCheck(t, err)
	_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
	historyCheck(t, err)
	get(masterPath, 404)
	get(entriesPath, 404)
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	historyCheck(t, svc.ChangeAssetAssociation(ctx, authorization.Actor{UserID: f.admin}, a.ID, authorization.AssetAssociationInput{Action: "return", EffectiveDate: date, Reason: "Returned while still covered", Revision: 0}))
	if data := get(masterPath, 200); data["asset"] != nil || data["assets"] != nil {
		t.Fatalf("returned Asset leaked: %v", data)
	}
	get(entriesPath, 409)
	// Coverage survives return; lack of field context must not be fabricated as No.
	assertContains(t, requestBody(t, f.router, cookie, "GET", "/leases/report?placement=unplaced", 200), "1 matching Leases")
	// A second valid session does not inherit another user's assignment.
	other, err := f.sessions.CreateMobile(ctx, f.admin, "Unassigned administrator")
	historyCheck(t, err)
	response := serviceAPIRequest(api, other, "GET", masterPath, nil)
	if response.Code != 404 {
		t.Fatalf("unassigned actor: %d %s", response.Code, response.Body.String())
	}
	for _, target := range []string{masterPath, entriesPath} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-FreeFSM-Session-Epoch", "00000000-0000-4000-8000-000000000001")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("stale epoch: %d %s", w.Code, w.Body.String())
		}
		assertNotContains(t, w.Body.String(), "lease_coverage")
	}
	_, err = f.pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp() WHERE device_name='Indicator reference'`)
	historyCheck(t, err)
	get(masterPath, 401)
	get(entriesPath, 401)
	historyCheck(t, f.sessions.RevokeMobile(ctx, other))
	response = serviceAPIRequest(api, other, "GET", masterPath, nil)
	if response.Code != 401 {
		t.Fatalf("revoked token: %d", response.Code)
	}
}

func fieldLeaseSchema(t *testing.T) func(string, any) {
	t.Helper()
	raw, err := os.ReadFile("../api/v1/reference/openapi.json")
	historyCheck(t, err)
	var spec map[string]any
	historyCheck(t, json.Unmarshal(raw, &spec))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	historyCheck(t, compiler.AddResource("https://field.test/openapi.json", spec))
	schemas := map[string]*jsonschema.Schema{}
	return func(name string, value any) {
		t.Helper()
		if schemas[name] == nil {
			schemas[name], err = compiler.Compile("https://field.test/openapi.json#/components/schemas/" + name)
			historyCheck(t, err)
		}
		raw, err := json.Marshal(value)
		historyCheck(t, err)
		decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		historyCheck(t, err)
		historyCheck(t, schemas[name].Validate(decoded))
		if name == "SyncServiceEntry" || name == "FieldLeaseServiceEntry" {
			object := decoded.(map[string]any)
			id := object["id"]
			if name == "SyncServiceEntry" {
				object["id"] = 1
			} else {
				object["id"] = "opaque-row"
			}
			if schemas[name].Validate(object) == nil {
				t.Fatalf("%s accepts the other identity representation", name)
			}
			object["id"] = id
		}
		// Every forbidden commercial facet must be rejected, not merely absent
		// in a conveniently empty fixture.
		for _, key := range []string{"lease_id", "lease_number", "starts_on", "ends_on", "notice", "rates", "files", "covered_assets", "lease_history"} {
			object := decoded.(map[string]any)
			object[key] = "private"
			if schemas[name].Validate(object) == nil {
				t.Fatalf("%s accepts forbidden %s", name, key)
			}
			delete(object, key)
		}
	}
}

func TestFieldLeaseIndicatorCompanyDateLifecycleHTTP(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		start, end, termination int
		covered                 bool
	}{
		{"scheduled", 1, 60, 999, false},
		{"current", -2, 60, 999, true},
		{"fixed last day", -2, 0, 999, true},
		{"month to month", -2, -1, 999, true},
		{"termination last day", -2, 60, 0, true},
		{"terminated", -2, 60, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := associationRepair(t)
			ctx := context.Background()
			c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Lifecycle Customer").SaveX(ctx)
			l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
			typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Lifecycle equipment").SaveX(ctx)
			a := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Equipment").SaveX(ctx)
			j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Lifecycle job").SaveX(ctx)
			f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(f.admin).SaveX(ctx)
			var today time.Time
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
			start := today.AddDate(0, 0, tc.start).Format(time.DateOnly)
			svc := authorization.New(f.pool)
			actor := authorization.Actor{UserID: f.admin}
			lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
			historyCheck(t, err)
			historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: today.AddDate(0, 0, tc.end).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
			zero, rent := int64(0), int64(5000)
			historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
			historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
			if tc.termination != 999 {
				end := today.AddDate(0, 0, tc.termination).Format(time.DateOnly)
				historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: end, EffectiveOn: end, Reason: "Inclusive boundary", OperationKey: newOperationKey()}))
			}
			token, err := f.sessions.CreateMobile(ctx, f.admin, "Lifecycle reader")
			historyCheck(t, err)
			r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			apiv1.NewRouter(f.pool, f.client, f.sessions).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Data struct {
					Asset struct {
						Coverage struct {
							Covered bool   `json:"covered"`
							AsOf    string `json:"as_of"`
						} `json:"lease_coverage"`
					} `json:"asset"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal(w.Body.Bytes(), &body))
			if body.Data.Asset.Coverage.Covered != tc.covered || body.Data.Asset.Coverage.AsOf == "" {
				t.Fatalf("coverage=%+v want %v", body.Data.Asset.Coverage, tc.covered)
			}
		})
	}
}
