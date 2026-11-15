package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
)

func TestEmailRoutedRetryAuthorityFaults(t *testing.T) {
	for _, fault := range []string{"stale-review", "revoked-authority", "revoked-session"} {
		t.Run(fault, func(t *testing.T) {
			f := permissionHTTPControlledMailInstanceFixture(t)
			ctx := context.Background()
			_, err := f.pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false,revision=revision+1`)
			historyCheck(t, err)
			p, err := emailpolicy.Read(ctx, f.pool, false)
			historyCheck(t, err)
			a := saveaction.Action{UserID: f.admin, ID: newOperationKey(), ExecutionToken: newOperationKey(), Path: authorization.EmailEnablePath, Revision: p.Review(), IntentHash: "routed-fault", Atomic: true, NoChange: true}
			store := saveaction.Store{Pool: f.pool}
			if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
				t.Fatalf("admit: %v %v", ok, err)
			}
			cookie := sessionCookie(t, ctx, f.sessions, f.admin)
			base := "/settings/enable-email/" + a.ID
			w := credentialPost(f.web, base+"/reconcile", url.Values{}, cookie)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "rejected") {
				t.Fatalf("reconcile: %d %s", w.Code, w.Body.String())
			}
			switch fault {
			case "stale-review":
				_, err = f.pool.Exec(ctx, `UPDATE restore_email_policy SET revision=revision+1`)
			case "revoked-authority":
				_, err = f.pool.Exec(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) SELECT company_id,'guardian@test','Guardian',password_hash,'admin',1 FROM users WHERE id=$1`, f.admin)
				historyCheck(t, err)
				_, err = f.pool.Exec(ctx, `UPDATE users SET authorization_role_id=2 WHERE id=$1`, f.admin)
			case "revoked-session":
				_, err = f.pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, f.admin)
			}
			historyCheck(t, err)
			var before string
			snapshotSQL := `SELECT jsonb_build_array((SELECT to_jsonb(p) FROM restore_email_policy p),(SELECT to_jsonb(a) FROM online_save_actions a WHERE operation_id=$1),(SELECT count(*) FROM online_save_effects WHERE operation_id=$1),(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text))::text`
			historyCheck(t, f.pool.QueryRow(ctx, snapshotSQL, a.ID).Scan(&before))
			w = credentialPost(f.web, base+"/retry", url.Values{"execution_token": {a.ExecutionToken}, "review": {a.Revision}}, cookie)
			if w.Code == 200 || w.Header().Get("X-FreeFSM-Disposition") == "saved" {
				t.Fatalf("fault authorized retry: %d %s", w.Code, w.Body.String())
			}
			r := httptest.NewRequest("GET", "/save-actions/"+a.ID, nil)
			r.AddCookie(cookie)
			receipt := httptest.NewRecorder()
			f.web.ServeHTTP(receipt, r)
			if strings.Contains(receipt.Body.String(), a.ExecutionToken) || strings.Contains(receipt.Body.String(), a.Revision) || strings.Contains(receipt.Body.String(), "routed-fault") {
				t.Fatal("generic receipt disclosed retry authority/private intent")
			}
			if fault == "revoked-session" && receipt.Code == 200 {
				t.Fatal("revoked session read receipt")
			}
			var after string
			historyCheck(t, f.pool.QueryRow(ctx, snapshotSQL, a.ID).Scan(&after))
			if before != after {
				t.Fatal("denied retry changed policy/audit/effect/attempt")
			}
		})
	}
}

func TestEmailBrowserLostResponseReauthentication(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and install browser prerequisites")
	}
	f := permissionHTTPControlledMailInstanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := f.pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false,revision=revision+1`)
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, f.sessions, f.admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(f.web))))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/email-faults.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("actual browser: %s", output)
	historyCheck(t, err)
	var audits, effects int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE action='email.enable'),(SELECT count(*) FROM online_save_effects e JOIN online_save_actions a USING(user_id,operation_id) WHERE a.request_path='/settings/enable-email')`).Scan(&audits, &effects))
	if audits != 1 || effects != 1 {
		t.Fatalf("reauthentication duplicated effect: audits=%d effects=%d", audits, effects)
	}
}
