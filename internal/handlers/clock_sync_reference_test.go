package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestClockSyncProtectedReferenceHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "workflow")
}

func TestClockSyncProtectedDenialHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "denial")
}

func TestClockSyncProtectedInstalledIdentityHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "identity")
}

func TestClockSyncProtectedDependencyRepairHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "dependencies")
}
func TestClockSyncProtectedExecutableHTTP(t *testing.T) { clockSyncProtectedReference(t, "cli") }

func TestClockSyncProtectedMalformedGPSRepairHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "gps-shape")
}

func TestClockSyncProtectedNullGPSRepairHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "gps-null")
}
func TestClockSyncProtectedDeniedGPSRepairHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "gps-denial")
}
func TestClockSyncProtectedStandaloneWithoutJobGrantHTTP(t *testing.T) {
	clockSyncProtectedReference(t, "solo")
}

func clockSyncProtectedReference(t *testing.T, scenario string) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	f, api, token, uid, role, jobs := syncFixture(t, 1)
	ctx := context.Background()
	if scenario == "identity" {
		if _, err := f.pool.Exec(ctx, `SELECT setval(pg_get_serial_sequence('time_entries','id'),9007199254740993,false)`); err != nil {
			t.Fatal(err)
		}
	}
	for _, permission := range []string{"time.view", "time.clock_in", "time.clock_out", "time.gps.capture", "time.gps.view"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'O')`, role, permission); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "solo" {
		if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission NOT LIKE 'time.%'`, role); err != nil {
			t.Fatal(err)
		}
	}
	api = clockSyncConformance(t, api)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1")
		}
		if r.URL.Path == "/__fixture/revoke-gps" {
			if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='time.gps.capture'`, role); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/revoke-time" {
			if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission LIKE 'time.%'`, role); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/restore" {
			if _, err := f.pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/token" {
			fresh, err := f.sessions.CreateMobile(ctx, uid, "Restored clocks")
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
			return
		}
		if r.URL.Path == "/__fixture/revoke" {
			if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, uid); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/lose-job" {
			if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, uid); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("X-Clock-Lose-Response") == "yes" && r.URL.Path == "/sync/operations" {
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			if response.Code != 200 {
				t.Errorf("interrupted clock submission=%d", response.Code)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = conn.Close()
			}
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token, "job": jobs[0], "scenario": scenario})
	run, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(run, "node", "../api/v1/testdata/offline-clocks-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("protected clocks: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

// Validate actual production requests/responses against the locally served3.1
// schemas. Invalid GPS may reject the envelope or produce its typed per-op400.
func clockSyncConformance(t *testing.T, api http.Handler) http.Handler {
	t.Helper()
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("https://clocks.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "ClockSyncEnvelope", "ClockReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := c.Compile("https://clocks.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ""
		switch {
		case r.URL.Path == "/sync/clocks":
			name = "ClockSyncEnvelope"
		case r.URL.Path == "/sync/operations":
			name = "OperationBatchEnvelope"
		case strings.HasPrefix(r.URL.Path, "/time-operations/"):
			name = "ClockReceiptEnvelope"
		}
		if name == "" {
			api.ServeHTTP(w, r)
			return
		}
		invalid := false
		if r.URL.Path == "/sync/operations" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Error(err)
				return
			}
			invalid = schemas["OperationBatch"].Validate(value) != nil
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		if response.Code >= 400 {
			name = "ErrorEnvelope"
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(response.Body.Bytes()))
		if err != nil {
			t.Error(err)
		} else if err = schemas[name].Validate(value); err != nil {
			t.Errorf("%s schema: %v", name, err)
		}
		if invalid && response.Code == 200 && !strings.Contains(response.Body.String(), `"status":400`) {
			t.Errorf("invalid Clock request acknowledged: %s", response.Body.String())
		}
		if r.URL.Path == "/sync/clocks" || r.URL.Path == "/sync/operations" {
			method := strings.ToLower(r.Method)
			operation := doc["paths"].(map[string]any)[r.URL.Path].(map[string]any)[method].(map[string]any)
			if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(response.Code)]; !ok {
				t.Errorf("undocumented status%d", response.Code)
			}
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
}
