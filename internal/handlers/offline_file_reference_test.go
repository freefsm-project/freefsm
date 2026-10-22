package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOfflineFilesProtectedReferenceHTTP(t *testing.T) {
	runOfflineFileReference(t, "offline-files-http.cjs", "PASS encrypted File publication")
}

func TestOfflineFilesTransportRecoveryHTTP(t *testing.T) {
	runOfflineFileReference(t, "offline-file-transport-http.cjs", "PASS encrypted transport")
}

func TestOfflineFilesProtectedNoopHistoryHTTP(t *testing.T) {
	runOfflineFileReference(t, "offline-file-noop-http.cjs", "PASS encrypted File no-op history")
}

func runOfflineFileReference(t *testing.T, script, success string) {
	t.Helper()
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	f := offlineFiles(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__fixture/corrupt-transfer":
			var in struct{ ID string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE file_transfer_chunks SET data=decode(repeat('78',octet_length(data)),'hex') WHERE transfer_id=$1`, in.ID); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		case "/__fixture/loss":
			if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.user); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		case "/__fixture/regrant":
			if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, f.job, f.user); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		case "/__fixture/restore":
			if _, err := f.pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`); err != nil {
				t.Error(err)
			}
			token, err := f.sessions.CreateMobile(ctx, f.user, "Restored Files")
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		case "/__fixture/reauthorize":
			if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, f.user); err != nil {
				t.Error(err)
			}
			token, err := f.sessions.CreateMobile(ctx, f.user, "New File client")
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		}
		if r.Header.Get("X-Lose-File-Response") == "yes" {
			response := httptest.NewRecorder()
			f.api.ServeHTTP(response, r)
			if response.Code != 200 && response.Code != 201 {
				t.Errorf("lost response did not commit: %d %s", response.Code, response.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = conn.Close()
			}
			return
		}
		f.api.ServeHTTP(w, r)
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": f.token})
	runCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/"+script)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("encrypted Files: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), success) {
		t.Fatal("reference assertions did not finish")
	}
}
