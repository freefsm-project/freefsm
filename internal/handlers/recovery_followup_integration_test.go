package handlers

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSetupSessionPublicationRechecksCommittedCredential(t *testing.T) {
	pool, client, _, _, _, _ := permissionHTTPFixture(t, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg := pool.Config().Copy()
	cfg.BeforeAcquire = func(ctx context.Context, _ *pgx.Conn) bool {
		once.Do(func() { close(reached) })
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	}
	issuingPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer issuingPool.Close()
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h := NewSetupHandler(pool, services.NewSessionService(issuingPool), services.NewUserService(client, pool), services.NewCompanySettingsService(client), &config.Config{SetupToken: "setup-secret"})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- credentialPost(h, "/setup", url.Values{"token": {"setup-secret"}, "name": {"Initial"}, "email": {"setup-publication@test"}, "password": {"InitialPassword1!"}, "confirm_password": {"InitialPassword1!"}}, nil)
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("setup did not reach post-commit session issuance")
	}
	u, err := services.NewUserService(client).GetByEmail(ctx, "setup-publication@test")
	if err != nil {
		t.Fatal("setup account was not committed before session issuance: ", err)
	}
	resets := services.NewPasswordResetService(client, pool)
	proof, err := resets.CreateToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = resets.Redeem(ctx, proof, "ReplacementPassword1!"); err != nil {
		t.Fatal(err)
	}
	unblock()
	w := <-result
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/login?error=") || len(w.Result().Cookies()) != 0 {
		t.Fatalf("stale setup acknowledged/published cookie: status=%d headers=%v", w.Code, w.Header())
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, u.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("post-reset stale setup sessions=%d: %v", n, err)
	}
	if err = services.NewUserService(client).Authenticate(ctx, u.Email, "ReplacementPassword1!"); err != nil {
		t.Fatal("setup overwrote replacement password: ", err)
	}
}

func TestRecoveryRefreshRunsInsideAdmissionAndFailureDoesNotClaim(t *testing.T) {
	pool, client, _, _, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	email := services.NewEmailService(services.NewCompanySettingsService(client))
	email.SetInstanceControl(control)
	var settingsQueries atomic.Int64
	client.CompanySettings.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			settingsQueries.Add(1)
			return next.Query(ctx, q)
		})
	}))
	resets := services.NewPasswordResetService(client, pool)
	if err = resets.Request(ctx, "admin@test", "http://localhost/reset-password"); err != nil {
		t.Fatal(err)
	}
	if err = control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	var generation atomic.Int64
	generation.Store(1)
	type observation struct {
		generation int64
		held       bool
	}
	observed := make(chan observation, 4)
	refresh := func() error {
		// If admission is held, a concurrent restore cannot pass its drain while
		// refreshing this process's generation. Failed refresh must not touch DB work.
		probe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		release, e := control.Maintenance(probe)
		if release != nil {
			release()
		}
		observed <- observation{generation.Load(), errors.Is(e, context.DeadlineExceeded)}
		return errors.New("injected generation refresh failure")
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- resets.RunDispatch(workerCtx, email, refresh) }()
	defer func() { stop(); <-done }()
	select {
	case o := <-observed:
		t.Fatalf("refresh ran before email admission: %+v", o)
	case <-time.After(1200 * time.Millisecond):
	}
	// Model the completed replacement generation before explicit email re-enable.
	generation.Store(2)
	if err = control.EnableEmail(); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-observed:
		if o.generation != 2 || !o.held {
			t.Fatalf("refresh crossed restore boundary without admission: %+v", o)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not refresh after re-enable")
	}
	var clean bool
	if err = pool.QueryRow(ctx, `SELECT claimed_until IS NULL AND completed_at IS NULL AND NOT EXISTS(SELECT 1 FROM password_reset_tokens WHERE user_id=$1) FROM password_recovery_requests`, admin).Scan(&clean); err != nil || !clean {
		t.Fatalf("failed refresh claimed/sent work: clean=%v %v", clean, err)
	}
	if n := settingsQueries.Load(); n != 0 {
		t.Fatalf("settings used before successful generation refresh: %d queries", n)
	}
}

func TestRecoveryLookupStorageFailureKeepsDurableRequestResumable(t *testing.T) {
	pool, client, _, _, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	resets := services.NewPasswordResetService(client, pool)
	email := services.NewEmailService(services.NewCompanySettingsService(client))
	if err = resets.Request(ctx, "admin@test", "http://localhost/reset-password"); err != nil {
		t.Fatal(err)
	}
	// Only the locked user SELECT fails. The durable queue and its completion
	// connection remain usable, reproducing the previous terminal misclassification.
	if _, err = pool.Exec(ctx, `ALTER TABLE users RENAME TO recovery_lookup_users;
 CREATE FUNCTION recovery_lookup_failure() RETURNS bigint LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected locked user lookup failure'; END $$;
 CREATE VIEW users AS SELECT id,company_id,email,is_active,credential_revision+recovery_lookup_failure() AS credential_revision FROM recovery_lookup_users`); err != nil {
		t.Fatal(err)
	}
	repair := func() {
		if _, e := pool.Exec(ctx, `DROP VIEW users; ALTER TABLE recovery_lookup_users RENAME TO users; DROP FUNCTION recovery_lookup_failure()`); e != nil {
			t.Fatal(e)
		}
	}
	repaired := false
	defer func() {
		if !repaired {
			repair()
		}
	}()
	worked, dispatchErr := resets.DispatchOne(ctx, email)
	var incomplete bool
	if err = pool.QueryRow(ctx, `SELECT completed_at IS NULL AND email='admin@test' AND base_url<>'' AND NOT EXISTS(SELECT 1 FROM password_reset_tokens WHERE user_id=$1) FROM password_recovery_requests`, admin).Scan(&incomplete); err != nil {
		t.Fatal(err)
	}
	if !worked || dispatchErr == nil || errors.Is(dispatchErr, services.ErrInvalidRecovery) || !incomplete {
		t.Fatalf("lookup failure discarded durable request: worked=%v err=%v incomplete=%v", worked, dispatchErr, incomplete)
	}
	repair()
	repaired = true
	if _, err = pool.Exec(ctx, `UPDATE password_recovery_requests SET claimed_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	message := make(chan []byte, 1)
	smtpDone := make(chan error, 1)
	go func() { b, e := receiveTestSMTP(listener); message <- b; smtpDone <- e }()
	if worked, err = resets.DispatchOne(ctx, email); err != nil || !worked {
		t.Fatalf("repaired retry=%v %v", worked, err)
	}
	b := <-message
	if err = <-smtpDone; err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(b)
	if len(token) != 2 {
		t.Fatal("retry did not send proof")
	}
	if id, e := resets.Validate(ctx, string(token[1])); e != nil || id != admin {
		t.Fatalf("retried proof invalid: id=%d err=%v", id, e)
	}
}

func TestRecoveryDispatchInstanceLifecycleDrainGateAndCurrentIdentityResume(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherProcess, err := instancecontrol.New(control.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	current := client.User.Create().SetCompanyID(company).SetName("Current recipient").SetEmail("current-recipient@test").SetRole("tech").SetPasswordHash("unchanged").SaveX(ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	email := services.NewEmailService(services.NewCompanySettingsService(client))
	email.SetInstanceControl(control)
	resets := services.NewPasswordResetService(client, pool)
	for _, address := range []string{"admin@test", "admin@test", current.Email} {
		if err = resets.Request(ctx, address, "http://localhost/reset-password"); err != nil {
			t.Fatal(err)
		}
	}
	assertQueue := func(claimed, complete int) {
		t.Helper()
		var gotClaimed, gotComplete int
		if e := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE claimed_until IS NOT NULL),count(*) FILTER(WHERE completed_at IS NOT NULL) FROM password_recovery_requests`).Scan(&gotClaimed, &gotComplete); e != nil || gotClaimed != claimed || gotComplete != complete {
			t.Fatalf("queue claimed/completed=%d/%d want=%d/%d err=%v", gotClaimed, gotComplete, claimed, complete, e)
		}
	}
	// Before any attempt, both production gates reject without even claiming.
	maintenanceRelease, err := otherProcess.Maintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worked, e := resets.DispatchOne(ctx, email); worked || !errors.Is(e, instancecontrol.ErrMaintenance) {
		t.Fatalf("closed maintenance admitted dispatch: %v %v", worked, e)
	}
	assertQueue(0, 0)
	if err = control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	if err = otherProcess.ClearMaintenance(); err != nil {
		t.Fatal(err)
	}
	maintenanceRelease()
	if worked, e := resets.DispatchOne(ctx, email); worked || !errors.Is(e, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("disabled mail claimed work: %v %v", worked, e)
	}
	assertQueue(0, 0)
	// Real protected HTTP action explicitly enables email; no direct gate bypass.
	router := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()}, control)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	w := credentialPost(router, "/settings/enable-email", nil, adminCookie)
	if w.Code != 303 || control.EmailDisabled() {
		t.Fatalf("explicit enable failed: %d", w.Code)
	}
	delayed := delayedRecoverySMTP{listener, make(chan struct{}), make(chan struct{})}
	allowSMTP := sync.OnceFunc(func() { close(delayed.release) })
	defer allowSMTP()
	smtpResult := make(chan error, 1)
	go func() { _, e := receiveTestSMTP(delayed); smtpResult <- e }()
	dispatchDone := make(chan error, 1)
	go func() { _, e := resets.DispatchOne(ctx, email); dispatchDone <- e }()
	select {
	case <-delayed.entered:
	case <-ctx.Done():
		t.Fatal("admitted SMTP never started")
	}
	endMaintenance := make(chan struct{})
	releaseMaintenance := sync.OnceFunc(func() { close(endMaintenance) })
	defer releaseMaintenance()
	drainDone := make(chan error, 1)
	maintenanceReleased := make(chan struct{})
	go func() {
		defer close(maintenanceReleased)
		release, e := otherProcess.Maintenance(ctx)
		drainDone <- e
		if e == nil {
			<-endMaintenance
			release()
		}
	}()
	// Observe persisted maintenance intent before asserting its drain still waits.
	for {
		_, release, e := control.Enter(ctx)
		if errors.Is(e, instancecontrol.ErrMaintenance) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		release()
		select {
		case <-ctx.Done():
			t.Fatal("maintenance intent not visible")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case e := <-drainDone:
		t.Fatalf("maintenance passed live SMTP admission: %v", e)
	default:
	}
	if worked, e := resets.DispatchOne(ctx, email); worked || !errors.Is(e, instancecontrol.ErrMaintenance) {
		t.Fatalf("new dispatch bypassed maintenance intent: %v %v", worked, e)
	}
	assertQueue(1, 0)
	allowSMTP()
	if err = <-dispatchDone; err != nil {
		t.Fatal(err)
	}
	if err = <-smtpResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("maintenance failed to drain completed dispatch")
	}
	assertQueue(1, 1)
	if err = control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	// A restored/current identity decision makes one pending request obsolete.
	if err = authorization.New(pool).EditIdentity(ctx, authorization.Actor{UserID: admin}, admin, "Admin", "current-admin@test"); err != nil {
		t.Fatal(err)
	}
	if err = otherProcess.ClearMaintenance(); err != nil {
		t.Fatal(err)
	}
	releaseMaintenance()
	<-maintenanceReleased
	if worked, e := resets.DispatchOne(ctx, email); worked || !errors.Is(e, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("restore email gate bypassed: %v %v", worked, e)
	}
	assertQueue(1, 1)
	// Observe every later socket: disabled/stale work must not produce a message.
	messages := make(chan []byte, 2)
	mailErrors := make(chan error, 2)
	go func() {
		for i := 0; i < 2; i++ {
			b, e := receiveTestSMTP(listener)
			messages <- b
			mailErrors <- e
			if e != nil {
				return
			}
		}
	}()
	w = credentialPost(router, "/settings/enable-email", nil, adminCookie)
	if w.Code != 303 || control.EmailDisabled() {
		t.Fatalf("resume enable failed: %d", w.Code)
	}
	for i := 0; i < 2; i++ {
		if worked, e := resets.DispatchOne(ctx, email); !worked || e != nil {
			t.Fatalf("resume %d: %v %v", i, worked, e)
		}
	}
	var message []byte
	select {
	case message = <-messages:
	case <-ctx.Done():
		t.Fatal("current queued identity never mailed")
	}
	if err = <-mailErrors; err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(message)
	if len(token) != 2 || !strings.Contains(string(message), current.Email) {
		t.Fatalf("mailed wrong queued identity: %s", message)
	}
	if id, e := resets.Validate(ctx, string(token[1])); e != nil || id != current.ID {
		t.Fatalf("resumed proof not bound to current identity: id=%d err=%v", id, e)
	}
	assertQueue(3, 3)
	listener.Close()
	if b := <-messages; len(b) != 0 {
		t.Fatal("disabled or obsolete queue produced extra mail")
	}
}
