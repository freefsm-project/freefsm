package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

func onboardingPost(router http.Handler, cookie *http.Cookie, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func pendingInvitationHTTP(t *testing.T, pool *pgxpool.Pool, client *ent.Client, company, admin int64, suffix string) (*ent.User, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Invitation "+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	actorCtx := authorization.WithActor(ctx, auth, authorization.Actor{UserID: admin})
	u, err := services.NewUserService(client).Create(actorCtx, services.UserCreateParams{CompanyID: company, Name: "Invitee " + suffix, Email: suffix + "@example.test", RoleID: role, SendWelcomeEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	token, err := services.NewInvitationService(client, pool).CreateInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u, token, actorCtx
}

func TestInvitationLifecycleAndConcurrentRedemptionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u, token, actorCtx := pendingInvitationHTTP(t, pool, client, company, admin, "lifecycle21")
	var lifetime float64
	if err := pool.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-created_at) FROM invitation_tokens WHERE user_id=$1`, u.ID).Scan(&lifetime); err != nil || lifetime != 259200 {
		t.Fatalf("invitation lifetime %v: %v", lifetime, err)
	}
	post := func(token, password string) *httptest.ResponseRecorder {
		return onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {password}, "confirm_password": {password}})
	}
	if w := post(token, "weak"); w.Code != 200 || !strings.Contains(w.Body.String(), "password must") {
		t.Fatalf("weak password: %d %s", w.Code, w.Body.String())
	}
	invites := services.NewInvitationService(client, pool)
	if _, err := invites.ValidateInvite(ctx, token); err != nil {
		t.Fatal("weak password consumed invitation", err)
	}
	_, newToken, err := invites.RenewPendingInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := post(token, "ChosenPassword1!"); w.Code != 400 {
		t.Fatal("replaced invitation accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE invitation_tokens SET expires_at=clock_timestamp() WHERE user_id=$1 AND consumed_at IS NULL`, u.ID); err != nil {
		t.Fatal(err)
	}
	if w := post(newToken, "ChosenPassword1!"); w.Code != 400 {
		t.Fatal("expired invitation accepted")
	}
	_, newToken, err = invites.RenewPendingInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Recovery material in the distinct recovery table never authenticates an invite.
	reset := "wrong-purpose-recovery-21"
	resetHash := sha256.Sum256([]byte(reset))
	client.PasswordResetToken.Create().SetCompanyID(company).SetUserID(u.ID).SetTokenHash(hex.EncodeToString(resetHash[:])).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	if w := post(reset, "ChosenPassword1!"); w.Code != 400 {
		t.Fatal("wrong-purpose invitation accepted")
	}
	// A new application instance uses the persisted token and a fresh limiter.
	router = New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()})
	start := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() { <-start; results <- post(newToken, "ChosenPassword1!").Code }()
	}
	close(start)
	a, b := <-results, <-results
	if !(a == 303 && b == 400 || a == 400 && b == 303) {
		t.Fatalf("concurrent redemption statuses %d,%d", a, b)
	}
	got := client.User.GetX(ctx, u.ID)
	if !got.IsActive || got.ForcePasswordChange || got.OnboardingCompletedAt == nil || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("ChosenPassword1!")) != nil {
		t.Fatal("activation did not persist chosen credential without extra forced change")
	}
}

func TestInvitationRedemptionRechecksPendingDecisionAfterFenceHTTP(t *testing.T) {
	for _, decision := range []string{"disable", "identity", "role", "password"} {
		t.Run(decision, func(t *testing.T) {
			pool, client, router, _, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			u, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "race21"+decision)
			fence, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(ctx)
			if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("redemption did not wait for account decision fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			query := map[string]string{"disable": `UPDATE users SET is_active=false WHERE id=$1`, "identity": `UPDATE users SET email='new-identity@example.test' WHERE id=$1`, "role": `UPDATE users SET authorization_role_id=1 WHERE id=$1`, "password": `UPDATE users SET password_hash='newer-admin-credential',force_password_change=true WHERE id=$1`}[decision]
			if _, err := fence.Exec(ctx, query, u.ID); err != nil {
				t.Fatal(err)
			}
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if w := <-done; w.Code != 400 {
				t.Fatalf("newer %s decision overwritten: %d %s", decision, w.Code, w.Body.String())
			}
			if client.User.GetX(ctx, u.ID).IsActive {
				t.Fatal("stale redemption activated account")
			}
		})
	}
}

func TestInvitationActivationAuditFailureRollsBackHTTP(t *testing.T) {
	pool, client, router, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Invitation", nil)
	if err != nil {
		t.Fatal(err)
	}
	actorCtx := authorization.WithActor(ctx, auth, authorization.Actor{UserID: admin})
	u, err := services.NewUserService(client).Create(actorCtx, services.UserCreateParams{CompanyID: company, Name: "Invitee", Email: "audit21@example.test", RoleID: role, SendWelcomeEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	token, err := services.NewInvitationService(client).CreateInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_invitation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.invitation.accept' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_invitation_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_invitation_audit()`); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}}
	w := onboardingPost(router, nil, "/accept-invite", values)
	if w.Code != 500 {
		t.Fatalf("audit failure activation status %d, want 500", w.Code)
	}
	if got := client.User.GetX(ctx, u.ID); got.IsActive || got.PasswordHash != u.PasswordHash {
		t.Fatal("audit failure partially activated account")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_invitation_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	w = onboardingPost(router, nil, "/accept-invite", values)
	if w.Code != 303 {
		t.Fatalf("retry activation status %d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.invitation.accept' AND target_id=$1 AND actor_id=$1 AND scope='O' AND role_id=$2`, u.ID, role).Scan(&count); err != nil || count != 1 {
		t.Fatalf("activation audit count=%d error=%v", count, err)
	}
	if w := onboardingPost(router, nil, "/accept-invite", values); w.Code != 400 {
		t.Fatalf("used invitation accepted: %d", w.Code)
	}
}

func TestInvitationKnownUnavailableMailCreatesNoAccountHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	role, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Onboarding", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := onboardingPost(router, cookie, "/users", url.Values{"name": {"Pending person"}, "email": {"pending21@example.test"}, "role_id": {fmt.Sprint(role)}, "send_welcome_email": {"on"}})
	location, _ := url.QueryUnescape(w.Header().Get("Location"))
	if w.Code != 303 || !strings.Contains(strings.ToLower(location), "manual") {
		t.Fatalf("unavailable mail must offer manual onboarding: %d %s", w.Code, location)
	}
	if client.User.Query().Where(user.EmailEQ("pending21@example.test")).ExistX(ctx) {
		t.Fatal("known unavailable mail created an account")
	}
}

func TestInvitationPendingDisableInvalidatesLinkHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	actorCtx := authorization.WithActor(ctx, auth, authorization.Actor{UserID: admin})
	u, err := services.NewUserService(client).Create(actorCtx, services.UserCreateParams{CompanyID: company, Name: "Pending disable", Email: "disable21@example.test", RoleID: role, SendWelcomeEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	invites := services.NewInvitationService(client)
	token, err := invites.CreateInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/users/%d/disable", u.ID), nil, 303)
	if client.User.GetX(ctx, u.ID).IsActive {
		t.Fatal("pending disable activated account without chosen password")
	}
	w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
	if w.Code != 400 {
		t.Fatalf("disabled pending link accepted: %d", w.Code)
	}
	// An explicit Administrator resend is the new onboarding decision.
	_, fresh, err := invites.RenewPendingInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	w = onboardingPost(router, nil, "/accept-invite", url.Values{"token": {fresh}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
	if w.Code != 303 {
		t.Fatalf("explicit resend did not restore onboarding: %d %s", w.Code, w.Body.String())
	}
}

func TestInvitationUnexpectedMailFailureRetainsResendablePendingHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, port); err != nil {
		t.Fatal(err)
	}
	role, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Mail failure", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := onboardingPost(router, cookie, "/users", url.Values{"name": {"Failed mail"}, "email": {"failure21@example.test"}, "role_id": {fmt.Sprint(role)}, "send_welcome_email": {"on"}})
	location, _ := url.QueryUnescape(w.Header().Get("Location"))
	if w.Code != 303 || !strings.Contains(location, "pending") || !strings.Contains(location, "resend") {
		t.Fatalf("actionable pending outcome missing: %d %s", w.Code, location)
	}
	u := client.User.Query().Where(user.EmailEQ("failure21@example.test")).OnlyX(ctx)
	if u.IsActive || u.WelcomeEmailSentAt != nil || u.OnboardingCompletedAt != nil {
		t.Fatal("failed transport invented delivery or activation")
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/users/%d", u.ID), 200)
	assertContains(t, body, "Resend Welcome")
}

func TestInvitationUpgradeRetainsPendingWithoutInventedActivationHTTP(t *testing.T) {
	ctx := context.Background()
	var pending, active int64
	pool, client, router, sessions, company, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		// Migration 048 used created_at as a fallback completion timestamp even
		// when delivery/token preparation had failed. Preserve that raw evidence.
		if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,is_active,welcome_email_sent_at,created_at,onboarding_completed_at) SELECT id,'Legacy pending','legacy-pending21@example.test','legacy-pending-hash','tech',false,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z' FROM companies LIMIT 1 RETURNING id`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,is_active,created_at,onboarding_completed_at) SELECT id,'Legacy active','legacy-active21@example.test','legacy-active-hash','tech',true,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z' FROM companies LIMIT 1 RETURNING id`).Scan(&active); err != nil {
			t.Fatal(err)
		}
	})
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/users/%d", pending), 200)
	assertContains(t, body, "Resend Welcome")
	u := client.User.GetX(ctx, pending)
	if u.IsActive || u.PasswordHash != "legacy-pending-hash" || u.WelcomeEmailSentAt == nil {
		t.Fatal("migration changed recorded pending evidence")
	}
	if u := client.User.GetX(ctx, active); !u.IsActive || u.PasswordHash != "legacy-active-hash" {
		t.Fatal("migration changed active account")
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM onboarding_migration_review WHERE user_id=$1 AND recorded_completed_at='2024-01-01T00:00:00Z')`, pending).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy timestamp evidence lost: %v", err)
	}
	actorCtx := authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin})
	_, token, err := services.NewInvitationService(client, pool).RenewPendingInvite(actorCtx, company, pending)
	if err != nil {
		t.Fatal(err)
	}
	w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
	if w.Code != 303 {
		t.Fatalf("reviewed explicit resend failed: %d %s", w.Code, w.Body.String())
	}
}

func TestOnboardingPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	mail := make(chan []byte, 1)
	mailErr := make(chan error, 1)
	go func() {
		message, err := receiveTestSMTP(listener)
		if err != nil {
			mailErr <- err
		} else {
			mail <- message
		}
	}()
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := onboardingPost(router, cookie, "/users", url.Values{"name": {"Browser invited"}, "email": {"browser-invited21@example.test"}, "role_id": {"1"}, "send_welcome_email": {"on"}})
	location, _ := url.QueryUnescape(w.Header().Get("Location"))
	if w.Code != 303 || !strings.Contains(location, "mail transport accepted") {
		t.Fatalf("SMTP handoff outcome: %d %s", w.Code, location)
	}
	var message []byte
	select {
	case message = <-mail:
	case err := <-mailErr:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("no welcome SMTP message")
	}
	match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(message)
	if len(match) != 2 {
		t.Fatal("SMTP invitation link missing")
	}
	invited := client.User.Query().Where(user.EmailEQ("browser-invited21@example.test")).OnlyX(ctx)
	if invited.IsActive || invited.WelcomeEmailSentAt == nil {
		t.Fatal("accepted mail must retain pending account and record handoff")
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(cmdCtx, "node", "testdata/browser/onboarding.cjs", server.URL, cookie.Value, string(match[1])).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh application reads the durable outcome, not browser-local state.
	restarted := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()})
	for _, email := range []string{"browser-invited21@example.test", "browser-manual21@example.test"} {
		u := client.User.Query().Where(user.EmailEQ(email)).OnlyX(ctx)
		if !u.IsActive || u.ForcePasswordChange || u.OnboardingCompletedAt == nil {
			t.Fatalf("incomplete persisted onboarding for %s", email)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("BrowserChosenPassword1!")); err != nil {
			t.Fatal(err)
		}
		assertContains(t, requestBody(t, restarted, cookie, "GET", fmt.Sprintf("/users/%d", u.ID), 200), email)
	}
}

func TestManualOnboardingRequiresCredentialReplacementAcrossRoleAndAPIHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Manual empty Role", nil)
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	w := onboardingPost(router, adminCookie, "/users", url.Values{"name": {"Manual restriction"}, "email": {"manual-restricted21@example.test"}, "role_id": {fmt.Sprint(role)}, "password": {"TemporaryPassword1!"}, "confirm_password": {"TemporaryPassword1!"}})
	if w.Code != 303 {
		t.Fatalf("manual create: %d %s", w.Code, w.Body.String())
	}
	u := client.User.Query().Where(user.EmailEQ("manual-restricted21@example.test")).OnlyX(ctx)
	if !u.IsActive || !u.ForcePasswordChange {
		t.Fatal("manual creation omitted restricted credential state")
	}
	login := onboardingPost(router, nil, "/login", url.Values{"email": {u.Email}, "password": {"TemporaryPassword1!"}})
	if login.Code != 303 || login.Header().Get("Location") != "/change-password" {
		t.Fatalf("temporary login: %d %s", login.Code, login.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == "session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("missing restricted browser session")
	}
	// Even explicit promotion to protected Administrator cannot bypass replacement.
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/customers", "/users/new", "/settings/authorization", "/preferences"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		if response.Code != 303 || response.Header().Get("Location") != "/change-password" {
			t.Fatalf("restricted URL %s escaped: %d", path, response.Code)
		}
	}
	mobile, err := sessions.CreateMobile(ctx, u.ID, "Restricted fixture")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Authorization", "Bearer "+mobile)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, r)
	if response.Code != 403 && response.Code != 401 {
		t.Fatalf("restricted field credential escaped: %d %s", response.Code, response.Body.String())
	}
}

func TestInvitationAndManualCreationRequireSealedAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	pending, _, _ := pendingInvitationHTTP(t, pool, client, company, admin, "sealed21")
	viewer := client.User.Create().SetCompanyID(company).SetName("Legacy admin label").SetEmail("legacy-label21@example.test").SetPasswordHash("not-a-password").SetRole("admin").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, viewer.ID)
	for _, path := range []string{"/users", fmt.Sprintf("/users/%d/resend-welcome", pending.ID)} {
		w := onboardingPost(router, cookie, path, url.Values{"name": {"Denied"}, "email": {"denied21@example.test"}, "role_id": {"1"}, "password": {"TemporaryPassword1!"}, "confirm_password": {"TemporaryPassword1!"}, "send_welcome_email": {"on"}})
		if w.Code != 403 {
			t.Fatalf("unsealed onboarding %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if client.User.Query().Where(user.EmailEQ("denied21@example.test")).ExistX(ctx) {
		t.Fatal("denied account persisted")
	}
}

func TestInvitationRechecksPolicyAtCommitWithoutConsumingHTTP(t *testing.T) {
	pool, client, router, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "policy21")
	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(ctx)
	if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invitation did not wait for policy fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := fence.Exec(ctx, `UPDATE company_settings SET password_min_length=32 WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 200 || !strings.Contains(w.Body.String(), "at least 32") {
		t.Fatalf("policy race must show current validation: %d %s", w.Code, w.Body.String())
	}
	if client.User.GetX(ctx, u.ID).IsActive {
		t.Fatal("old policy activated user")
	}
	if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, token); err != nil {
		t.Fatal("validation consumed invitation", err)
	}
	password := "LongChosenPasswordMeetsCurrentPolicy1!"
	w = onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {password}, "confirm_password": {password}})
	if w.Code != 303 {
		t.Fatalf("valid retry failed: %d %s", w.Code, w.Body.String())
	}
}
