package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestJobSyncReferenceOpenAPI(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}
	for _, kind := range []string{"customer", "contact", "location", "asset", "project"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.Context})
	}
	uid, role, cookie := f.reader(t, grants)
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Reference Customer").SetNotes("PRIVATE-INTERNAL").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Service Location").SetLatitude(41.25).SetLongitude(-87.75).SaveX(ctx)
	contact := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("Explicit Contact").SaveX(ctx)
	p := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetName("Operational Project").SaveX(ctx)
	a := f.asset(t, c.ID, "REFERENCE-ASSET")
	if _, err := f.pool.Exec(ctx, `UPDATE assets SET location_id=$1, installed_at='2025-01-02', warranty_expires='2027-01-02' WHERE id=$2`, l.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	var statusID int64
	if err := f.pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.company_id=$1 AND w.object_type='job' AND s.category_key='job:new' LIMIT 1`, f.company).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetCustomerContactID(contact.ID).SetLocationID(l.ID).SetProjectID(p.ID).SetAssetID(a.ID).SetStatusID(statusID).SetJobType("Reference Job").SaveX(ctx)
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET start_time='2026-10-14T10:00:00Z',end_time='2026-10-14T11:00:00Z',due_date='2026-10-15',arrival_window_start='2026-10-14T09:30:00Z',arrival_window_end='2026-10-14T10:30:00Z' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	other := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Second reference Job").SaveX(ctx)
	f.client.JobAssignment.Create().SetJobID(other.ID).SetUserID(uid).SaveX(ctx)
	policy := config.APIConfig{}.Defaults()
	policy.SyncPerClient = 100
	api := apiv1.NewRouterWithLimits(f.pool, f.client, f.sessions, policy, nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatal("expected OpenAPI 3.1")
	}
	if _, ok := doc["paths"].(map[string]any)["/sync/jobs"]; !ok {
		t.Fatal("served OpenAPI does not publish Job sync")
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://sync.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	validate := func(fragment string, body []byte) {
		t.Helper()
		schema, err := compiler.Compile("https://sync.test/openapi.json#" + fragment)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	paired := pairingCall(api, "/pairings/redeem", approvedPairing(t, api, f.router, cookie))
	var issued struct {
		Data struct {
			Token string `json:"token"`
		}
	}
	if err := json.Unmarshal(paired.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if paired.Code != 201 || issued.Data.Token == "" {
		t.Fatal("pairing failed")
	}
	operation := doc["paths"].(map[string]any)["/sync/jobs"].(map[string]any)["get"].(map[string]any)
	if security, ok := operation["security"]; ok && len(security.([]any)) == 0 {
		t.Fatal("sync must require bearer")
	}
	if len(doc["security"].([]any)) == 0 {
		t.Fatal("missing default bearer security")
	}
	// Validate parameter schemas from the served operation, including type conversion
	// required by OpenAPI query serialization. Cross-parameter restrictions are explicit.
	validQuery := func(query string) bool {
		q, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
		if err != nil {
			return false
		}
		if q.Has("cursor") && q.Has("page") {
			return false
		}
		for name, values := range q {
			if len(values) != 1 {
				return false
			}
			found := false
			for i, raw := range operation["parameters"].([]any) {
				param := raw.(map[string]any)
				if param["name"] != name {
					continue
				}
				found = true
				schema, err := compiler.Compile(fmt.Sprintf("https://sync.test/openapi.json#/paths/~1sync~1jobs/get/parameters/%d/schema", i))
				if err != nil {
					t.Fatal(err)
				}
				var value any = values[0]
				if param["schema"].(map[string]any)["type"] == "integer" {
					n, err := strconv.Atoi(values[0])
					if err != nil {
						return false
					}
					value = n
				}
				if schema.Validate(value) != nil {
					return false
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	call := func(handler http.Handler, query, token string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/sync/jobs"+query, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("sync %s status %d want %d: %s", query, w.Code, want, w.Body.String())
		}
		if !validQuery(query) && w.Code != 400 {
			t.Fatal("schema-invalid query accepted")
		}
		responses := operation["responses"].(map[string]any)
		response, ok := responses[strconv.Itoa(w.Code)].(map[string]any)
		if !ok {
			t.Fatalf("undocumented status %d", w.Code)
		}
		fragment := fmt.Sprintf("/paths/~1sync~1jobs/get/responses/%d", w.Code)
		if ref, ok := response["$ref"].(string); ok {
			fragment = strings.TrimPrefix(ref, "#")
		}
		validate(fragment+"/content/application~1json/schema", w.Body.Bytes())
		if w.Code >= 400 && w.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing error request ID")
		}
		if (w.Code == 429 || w.Code == 503) && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry timing")
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("non-JSON sync response")
		}
		assertNotContains(t, w.Body.String(), "PRIVATE-INTERNAL")
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if want >= 400 {
			e := result["error"].(map[string]any)
			codes := map[int]string{400: "invalid_request", 401: "unauthorized", 403: "forbidden", 409: "resnapshot_required", 429: "rate_limit_exceeded", 503: "unavailable"}
			if e["code"] != codes[want] {
				t.Fatalf("status %d error code %v", want, e["code"])
			}
			if (want == 429 || want == 503) && (e["recovery_disposition"] != "retry" || e["retry_after_seconds"] == nil) {
				t.Fatal("retryable error missing recovery/timing")
			}
		}
		return result
	}
	token := issued.Data.Token
	capabilities := func(available bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "/capabilities", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("capabilities unavailable")
		}
		validate("/components/schemas/CapabilitiesEnvelope", w.Body.Bytes())
		var envelope struct {
			Data struct {
				Sync map[string]any `json:"sync"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.Sync["available"] != available {
			t.Fatal("sync discovery does not reflect current grants")
		}
	}
	capabilities(true)
	call(api, "", "", 401)
	first := call(api, "", token, 200)
	data := first["data"].(map[string]any)
	record := data["records"].([]any)[0].(map[string]any)
	masters := record["masters"].(map[string]any)
	// Each nested schema rejects disclosure and missing contract fields, not just
	// a static marker. Validate the actual emitted nested values and mutations.
	for name, value := range map[string]any{"SyncJob": record, "SyncCustomer": record["customer"], "SyncContact": masters["contact"], "SyncLocation": masters["location"], "SyncAsset": masters["asset"], "SyncAssetLocation": masters["asset"].(map[string]any)["location"], "SyncProject": masters["project"], "SyncMasters": masters} {
		raw, _ := json.Marshal(value)
		validate("/components/schemas/"+name, raw)
		schema, err := compiler.Compile("https://sync.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var mutated map[string]any
		if err := json.Unmarshal(raw, &mutated); err != nil {
			t.Fatal(err)
		}
		mutated["private_internal_notes"] = "forbidden"
		if schema.Validate(mutated) == nil {
			t.Fatalf("%s allows unknown nested disclosure", name)
		}
		delete(mutated, "private_internal_notes")
		if name != "SyncMasters" {
			required := doc["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)["required"].([]any)
			key := required[0].(string)
			delete(mutated, key)
			if schema.Validate(mutated) == nil {
				t.Fatalf("%s permits missing %s", name, key)
			}
		}
	}
	if _, err := time.LoadLocation(record["timezone"].(string)); err != nil {
		t.Fatal("invalid IANA zone")
	}
	for _, query := range []string{"?unknown=x", "?page_size=0", "?page_size=201", "?page_size=1.5", "?scope=other", "?cursor=", "?scope=assigned&scope=assigned", "?cursor=a&page=b"} {
		if validQuery(query) {
			t.Fatalf("invalid query passed schema: %s", query)
		}
		call(api, query, token, 400)
	}
	call(api, "?scope=all", token, 403)
	adminPair := pairingCall(api, "/pairings/redeem", approvedPairing(t, api, f.router, f.cookie))
	var broad struct {
		Data struct {
			Token string `json:"token"`
		}
	}
	if err := json.Unmarshal(adminPair.Body.Bytes(), &broad); err != nil {
		t.Fatal(err)
	}
	if adminPair.Code != 201 || broad.Data.Token == "" {
		t.Fatal("broad pairing failed")
	}
	all := call(api, "?scope=all", broad.Data.Token, 200)["data"].(map[string]any)["records"].([]any)
	if len(all) != 2 {
		t.Fatal("broad Job scope omitted unassigned Jobs")
	}
	for _, raw := range all {
		record := raw.(map[string]any)
		if _, ok := record["customer"]; ok || len(record["masters"].(map[string]any)) != 0 {
			t.Fatal("broad View expanded unassigned context")
		}
	}
	call(api, "?cursor=unknown", token, 409)
	call(api, "?page_size=200", token, 200)
	call(api, "?cursor="+url.QueryEscape(data["completion_cursor"].(string)), token, 200)
	page := call(api, "?page_size=1", token, 200)["data"].(map[string]any)
	if page["complete"] != false || page["completion_cursor"] != nil {
		t.Fatal("nonfinal page claims completion")
	}
	lastQuery := "?page_size=1&page=" + url.QueryEscape(page["next_page"].(string))
	last := call(api, lastQuery, token, 200)
	replayed := call(api, lastQuery, token, 200)
	lastJSON, _ := json.Marshal(last)
	replayJSON, _ := json.Marshal(replayed)
	if !bytes.Equal(lastJSON, replayJSON) {
		t.Fatal("replay changed page")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE projects SET deleted_at=now() WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	archived := call(api, "", token, 200)["data"].(map[string]any)["records"].([]any)[0].(map[string]any)["masters"].(map[string]any)
	if _, ok := archived["project"]; ok {
		t.Fatal("archived Project detail leaked")
	}
	raw, _ := json.Marshal(archived["project_reference"])
	validate("/components/schemas/SyncProjectReference", raw)
	// An injected real database failure must match the published retryable error.
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION sync_reference_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture storage failure'; END $$; CREATE TRIGGER sync_reference_fail BEFORE INSERT ON job_sync_snapshots FOR EACH ROW EXECUTE FUNCTION sync_reference_fail()`); err != nil {
		t.Fatal(err)
	}
	call(api, "", token, 503)
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER sync_reference_fail ON job_sync_snapshots`); err != nil {
		t.Fatal(err)
	}
	call(api, "", token, 200)
	// Production default budget, including documented Retry-After and error schema.
	limited := apiv1.NewRouter(f.pool, f.client, f.sessions)
	for i := 0; i < 12; i++ {
		call(limited, "", token, 200)
	}
	call(limited, "", token, 429)
	if _, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", nil, 1); err != nil {
		t.Fatal(err)
	}
	call(api, "", token, 403)
	capabilities(false)
}

func TestJobSyncReferencePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	f := associationRepair(t)
	ctx := context.Background()
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}}
	uid, role, cookie := f.reader(t, grants)
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Browser sync Customer").SetNotes("PRIVATE-INTERNAL").SaveX(ctx)
	var ids []int64
	for i := 0; i < 3; i++ {
		j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType(fmt.Sprintf("Persisted sync Job %d", i)).SaveX(ctx)
		f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
		ids = append(ids, j.ID)
	}
	policy := config.APIConfig{}.Defaults()
	policy.SyncPerClient = 100 // Failure/replay journey; default budget has separate HTTP coverage.
	mux := chi.NewRouter()
	mux.Mount("/api/v1", apiv1.NewRouterWithLimits(f.pool, f.client, f.sessions, policy, nil))
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Post("/__sync_fixture/{phase}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch chi.URLParam(r, "phase") {
		case "edit":
			_, err = f.pool.Exec(ctx, `UPDATE jobs SET job_type='Concurrent persisted edit' WHERE id=$1`, ids[1])
		case "remove":
			_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid)
		case "grants":
			_, err = authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", grants[:1], 1)
		case "revoke":
			_, err = f.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND kind='mobile'`, uid)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			t.Errorf("fixture phase: %v", err)
			http.Error(w, "fixture failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Mount("/", middleware.BrowserCSRF(middleware.CSRFToken(f.router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "testdata/browser/job-sync.cjs", server.URL)
	cmd.Env = append(os.Environ(), "PAIRING_BROWSER_COOKIE="+cookie.Value, "SYNC_ADMIN_BROWSER_COOKIE="+f.cookie.Value)
	out, err := cmd.CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var title string
	if err := f.pool.QueryRow(ctx, `SELECT job_type FROM jobs WHERE id=$1`, ids[1]).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Concurrent persisted edit" {
		t.Fatal("browser journey did not read persisted edit")
	}
}
