package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/crypto/bcrypt"
)

func TestRecoveryPersistedChromium(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("BrowserOriginal1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Recovery browser").SetEmail("recovery-browser@test").SetRole("tech").SetPasswordHash(string(hash)).SaveX(ctx)
	acting := sessionCookie(t, ctx, sessions, u.ID)
	other := sessionCookie(t, ctx, sessions, u.ID)
	mobile, err := sessions.CreateMobile(ctx, u.ID, "browser-test-phone")
	if err != nil {
		t.Fatal(err)
	}
	resets := services.NewPasswordResetService(client, pool)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetPasswordMinLength(16).SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	mailed := make(chan string, 1)
	evidenceToken := make(chan string, 1)
	go func() {
		message, e := receiveTestSMTP(listener)
		if e != nil {
			mailed <- ""
			return
		}
		m := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(message)
		if len(m) != 2 {
			mailed <- ""
			return
		}
		mailed <- string(m[1])
		evidenceToken <- string(m[1])
	}()
	mailbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case token := <-mailed:
			fmt.Fprint(w, token)
		case <-r.Context().Done():
		}
	}))
	defer mailbox.Close()
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = resets.RunDispatch(workerCtx, services.NewEmailService(services.NewCompanySettingsService(client)), nil)
	}()
	defer func() { stopWorker(); <-workerDone }()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	var leakedReferrers, referrerProbes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Referer(), "/reset-password?token=") {
			leakedReferrers.Add(1)
		}
		if r.URL.Query().Get("recovery-referrer-probe") == "1" {
			referrerProbes.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(cmdCtx, "node", "testdata/browser/recovery.cjs", server.URL, acting.Value, other.Value, adminCookie.Value, fmt.Sprint(u.ID), mailbox.URL).CombinedOutput()
	t.Logf("Chromium: %s", output)
	if leakedReferrers.Load() != 0 {
		t.Errorf("browser disclosed recovery bearer URL in %d actual HTTP Referer headers", leakedReferrers.Load())
	}
	if referrerProbes.Load() == 0 {
		t.Error("browser did not exercise same-origin referrer probe")
	}
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(client.User.GetX(ctx, u.ID).PasswordHash), []byte("BrowserFinalPassword1!")) != nil {
		t.Fatal("browser credential not persisted")
	}
	if client.User.GetX(ctx, u.ID).ForcePasswordChange {
		t.Fatal("forced change not completed")
	}
	if _, err = sessions.ValidateMobile(ctx, mobile); err == nil {
		t.Fatal("mobile survived")
	}
	if _, err = resets.Validate(ctx, <-evidenceToken); err == nil {
		t.Fatal("recovery survived")
	}
	// A rebuilt production router observes the committed credential, not browser state.
	restarted := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()})
	r := httptest.NewRequest("GET", "/change-password", nil)
	r.AddCookie(acting)
	w := httptest.NewRecorder()
	restarted.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("old session after restart=%d", w.Code)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_id=$1 AND action IN ('user.password.recover','user.password.change','user.password.require_change')`, u.ID).Scan(&n); err != nil || n != 4 {
		t.Fatalf("persisted audit=%d %v", n, err)
	}
}
