package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

func TestDisabledEmailForgotPasswordDoesNotDiscloseAccount(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	client.User.Create().SetCompanyID(77).SetEmail("present@example.test").SetName("Present").SetPasswordHash("unused").SetRole("admin").SaveX(ctx)
	dir := t.TempDir()
	control, err := instancecontrol.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	// Reopen to exercise the durable gate, rather than an in-memory setting.
	control, err = instancecontrol.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	email := services.NewEmailService(services.NewCompanySettingsService(client))
	email.SetInstanceControl(control)
	h := NewAuthHandler(pool, nil, services.NewUserService(client), nil, email, services.NewPasswordResetService(client), nil, nil, nil)
	var previous string
	for _, address := range []string{"present@example.test", "absent@example.test"} {
		r := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(url.Values{"email": {address}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ForgotPassword(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), instancecontrol.ErrEmailDisabled.Error()) {
			t.Fatalf("missing disabled notice: %s", w.Body.String())
		}
		if previous != "" && previous != w.Body.String() {
			t.Fatal("disabled response disclosed account existence")
		}
		previous = w.Body.String()
	}
	if got := client.PasswordResetToken.Query().CountX(ctx); got != 0 {
		t.Fatalf("created %d reset tokens while disabled", got)
	}
}

func TestMaintenanceDrainsHTTPWorkAndRequiresExplicitRecovery(t *testing.T) {
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := instancecontrol.New(control.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := control.OperationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer operation()
	if release, err := peer.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("competing operation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admitted := make(chan context.Context, 1)
	finish := make(chan struct{})
	done := make(chan struct{})
	handler := control.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admitted <- r.Context()
		select {
		case <-finish:
		case <-ctx.Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/write", nil).WithContext(ctx))
	}()
	var requestCtx context.Context
	select {
	case requestCtx = <-admitted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	type result struct {
		release func()
		err     error
	}
	maintenance := make(chan result, 1)
	go func() { release, err := peer.Maintenance(ctx); maintenance <- result{release, err} }()
	// Wait for persisted intent, while the original request still owns admission.
	for {
		_, release, err := control.Enter(ctx)
		if errors.Is(err, instancecontrol.ErrMaintenance) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		release()
		time.Sleep(time.Millisecond)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/normal", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("maintenance status = %d", w.Code)
	}
	// Nested email admission must finish rather than deadlock behind intent.
	_, nested, err := peer.Enter(requestCtx)
	if err != nil {
		t.Fatal(err)
	}
	nested()
	select {
	case r := <-maintenance:
		if r.release != nil {
			r.release()
		}
		t.Fatal("maintenance failed to drain active HTTP request")
	default:
	}
	close(finish)
	<-done
	r := <-maintenance
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.release()
	_, release, err := control.Enter(ctx)
	if release != nil {
		release()
	}
	if !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("release reopened unverified state: %v", err)
	}
	// Simulate engine restart with persistent state; recovery can reacquire.
	restarted, err := instancecontrol.New(control.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err = restarted.Maintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ClearMaintenance(); err != nil {
		t.Fatal(err)
	}
	release()
	w = httptest.NewRecorder()
	restarted.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/write", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("recovered status = %d", w.Code)
	}
}

func TestDisabledEmailBlocksWelcomeAndWorkerBeforeMutation(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	settings := services.NewCompanySettingsService(client)
	email := services.NewEmailService(settings)
	email.SetInstanceControl(control)
	h := NewUserHandler(services.NewUserService(client), email, services.NewInvitationService(client), settings, nil, nil)
	form := url.Values{"name": {"Invited"}, "email": {"invited@example.test"}, "role": {"tech"}, "send_welcome_email": {"on"}}
	r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(context.WithValue(ctx, middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: 77, Role: "admin"}))
	w := httptest.NewRecorder()
	h.Create(w, r)
	location, _ := url.QueryUnescape(w.Header().Get("Location"))
	if !strings.Contains(location, instancecontrol.ErrEmailDisabled.Error()) {
		t.Fatalf("missing disabled notice: %s", location)
	}
	if client.User.Query().CountX(ctx) != 0 || client.InvitationToken.Query().CountX(ctx) != 0 {
		t.Fatal("disabled welcome created account or invitation")
	}
	u, err := services.NewUserService(client).Create(ctx, services.UserCreateParams{CompanyID: 77, Name: "Pending", Email: "pending@example.test", Role: "tech", SendWelcomeEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	inviteSvc := services.NewInvitationService(client)
	if _, err := inviteSvc.CreateInvite(ctx, 77, u.ID); err != nil {
		t.Fatal(err)
	}
	before := client.InvitationToken.Query().OnlyX(ctx)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", strconv.FormatInt(u.ID, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w = httptest.NewRecorder()
	h.ResendWelcome(w, r)
	location, _ = url.QueryUnescape(w.Header().Get("Location"))
	if !strings.Contains(location, instancecontrol.ErrEmailDisabled.Error()) {
		t.Fatalf("resend missing disabled notice: %s", location)
	}
	after := client.InvitationToken.Query().OnlyX(ctx)
	if after.ID != before.ID || after.ConsumedAt != nil {
		t.Fatal("disabled resend replaced invitation")
	}
	workerService := delivery.New(pool, "https://example.test")
	workerService.SetInstanceControl(control)
	// A nil sender is intentional: disabled processing must never reach transport.
	if processed, err := workerService.ProcessOne(ctx, nil, nil); err != nil || processed {
		t.Fatalf("disabled worker: %v, %v", processed, err)
	}
	if err := email.SendEmail(ctx, "target@example.test", "Subject", "Body"); !errors.Is(err, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("transport gate: %v", err)
	}
	if err := control.EnableEmail(); err != nil {
		t.Fatal(err)
	}
	if err := email.CheckAvailability(ctx); err != nil {
		t.Fatal(err)
	}
}
