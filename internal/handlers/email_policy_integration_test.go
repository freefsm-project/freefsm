package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestSealedEmailPolicyHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPInstanceFixture(t)
	ctx := context.Background()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := admissionManager(t, pool, control)
	startup, err := manager.BeginStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = emailpolicy.Import(ctx, tx, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	control.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		if err := manager.RefreshConnections(); err != nil {
			return false, err
		}
		p, err := emailpolicy.Read(ctx, pool, false)
		return p.Enabled, err
	})
	if err = startup.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	router := control.Middleware(middleware.Company(services.NewCompanySettingsService(client))(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}, control)))
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings/mail", 200)
	if !strings.Contains(body, `action="/settings/enable-email"`) {
		t.Fatal("missing enable form")
	}
	p, err := emailpolicy.Read(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, p.Review()) {
		t.Fatal("enable form lacks current policy review")
	}
	post := func(id, revision string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/settings/enable-email", strings.NewReader(url.Values{"save_action_id": {id}, "save_revision": {revision}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post("", p.Review()); w.Code == 303 {
		t.Fatal("missing action accepted")
	}
	if w := post(newOperationKey(), "stale"); w.Code == 303 {
		t.Fatal("stale review accepted")
	}
	id := newOperationKey()
	w := post(id, p.Review())
	if w.Code != 303 || w.Header().Get("X-FreeFSM-Disposition") != "saved" {
		t.Fatalf("enable %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if control.EmailDisabled() {
		t.Fatal("confirmed database policy not visible")
	}
	if w := post(id, p.Review()); w.Code != 409 {
		t.Fatalf("duplicate dispatched %d", w.Code)
	}
	t.Run("composition-browser-reconcile-retry", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("set BROWSER_TESTS=1 and install browser prerequisites")
		}
		if _, err := pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false,revision=revision+1`); err != nil {
			t.Fatal(err)
		}
		policy, err := emailpolicy.Read(ctx, pool, false)
		if err != nil {
			t.Fatal(err)
		}
		a := saveaction.Action{UserID: admin, ID: newOperationKey(), ExecutionToken: newOperationKey(), Path: authorization.EmailEnablePath, Revision: policy.Review(), IntentHash: "composition-browser-reviewed-intent", Atomic: true, NoChange: true}
		store := saveaction.Store{Pool: pool}
		if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
			t.Fatalf("admission: %v %v", ok, err)
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(router))))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(browserCtx, "node", "testdata/browser/email-composition.cjs", server.URL, cookie.Value, a.ID).CombinedOutput()
		t.Logf("actual rendered script/routed browser: %s", out)
		if err != nil {
			t.Fatal(err)
		}
		var counts string
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text)::text||':'||(SELECT count(*) FROM online_save_effects WHERE operation_id=$1::uuid)::text`, a.ID).Scan(&counts); err != nil {
			t.Fatal(err)
		}
		if counts != "1:1" {
			t.Fatal(fmt.Sprintf("atomic audit/effect counts %s", counts))
		}
		receipt, err := store.Receipt(ctx, a)
		if err != nil || receipt.Disposition != "saved" {
			t.Fatalf("receipt: %+v %v", receipt, err)
		}
	})
	if _, err = pool.Exec(ctx, `DELETE FROM restore_email_policy`); err != nil {
		t.Fatal(err)
	}
	if !control.EmailDisabled() {
		t.Fatal("missing modern policy fell back to absent legacy marker")
	}
	if body := requestBody(t, router, cookie, "GET", "/settings/mail", 200); !strings.Contains(body, "Outbound email status is unavailable.") {
		t.Fatal("missing policy notice must say unavailable")
	}
}

// Real routed TestMail holds authority through SMTP. A pre-admitted enable and
// maintenance drain must both finish after that owned handoff is released.
func TestEmailCompositionTestMailDrain(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPInstanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := admissionManager(t, pool, control)
	startup, err := manager.BeginStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := emailpolicy.Import(ctx, tx, func() (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	control.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		if err := manager.RefreshConnections(); err != nil {
			return false, err
		}
		p, err := emailpolicy.Read(ctx, pool, false)
		return p.Enabled, err
	})
	if err := startup.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	router := control.Middleware(middleware.Company(services.NewCompanySettingsService(client))(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()}, control)))
	cookie := sessionCookie(t, ctx, sessions, admin)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	handoff, allow, sinkDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-allow:
		default:
			close(allow)
		}
	}()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			sinkDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		p := textproto.NewConn(conn)
		_ = p.PrintfLine("220 owned sink")
		accepted := false
		for {
			line, err := p.ReadLine()
			if err != nil {
				if accepted && err == io.EOF {
					err = nil
				}
				sinkDone <- err
				return
			}
			if line == "DATA" {
				_ = p.PrintfLine("354 send")
				if _, err := io.ReadAll(p.DotReader()); err != nil {
					sinkDone <- err
					return
				}
				close(handoff)
				<-allow
				_ = p.PrintfLine("250 accepted")
				accepted = true
			} else if line == "QUIT" {
				_ = p.PrintfLine("221 bye")
				sinkDone <- nil
				return
			} else {
				_ = p.PrintfLine("250 ok")
			}
		}
	}()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	mailCtx, mailLeave, err := control.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer mailLeave()
	enableCtx, enableLeave, err := control.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer enableLeave()
	post := func(c context.Context, path string, values url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode())).WithContext(c)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	mailDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		defer mailLeave()
		mailDone <- post(mailCtx, "/settings/test-email", url.Values{"recipient": {"recipient@example.test"}})
	}()
	select {
	case <-handoff:
	case <-ctx.Done():
		t.Fatal("SMTP handoff did not start")
	}
	policy, err := emailpolicy.Read(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	enableDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		defer enableLeave()
		enableDone <- post(enableCtx, authorization.EmailEnablePath, url.Values{"save_action_id": {newOperationKey()}, "save_revision": {policy.Review()}})
	}()
	maintenanceDone := make(chan error, 1)
	go func() {
		lease, err := control.BeginMaintenance(ctx)
		if err == nil {
			lease.Close()
		}
		maintenanceDone <- err
	}()
	close(allow)
	select {
	case w := <-mailDone:
		if w.Code != 200 {
			t.Fatalf("mail %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("TestMail deadlock")
	}
	select {
	case w := <-enableDone:
		if w.Code != 303 {
			t.Fatalf("enable %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("enable deadlock")
	}
	select {
	case err := <-maintenanceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("maintenance deadlock")
	}
	if err := <-sinkDone; err != nil {
		t.Fatal(err)
	}
}
