package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	chimw "github.com/go-chi/chi/v5/middleware"
)

func TestBearerAuthReloadsTrustedUserForMe(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Name: "DB User", Email: "db@example.test", Role: "tech", IsActive: true}}
	api := New(testDependencies(users, &fakeJobs{}))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "bEaReR valid-token")
	res := httptest.NewRecorder()
	api.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if users.gotID != 7 {
		t.Fatalf("GetByID id = %d, want 7", users.gotID)
	}
	if strings.Contains(res.Body.String(), "password") || !strings.Contains(res.Body.String(), `"role":"tech"`) {
		t.Fatalf("unexpected user response: %s", res.Body.String())
	}
}

func TestBearerAuthRejectsMissingCredentialsWithChallenge(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))
	res := httptest.NewRecorder()
	api.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/me", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
	if got := res.Header().Get("WWW-Authenticate"); got != `Bearer realm="freefsm-api"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func TestBearerAuthRevokesSessionWhenPasswordChangeIsRequired(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, IsActive: true, ForcePasswordChange: true}}
	sessions := &fakeSessions{token: "valid-token", userID: 7}
	deps := testDependencies(users, &fakeJobs{})
	deps.Sessions = sessions
	api := New(deps)

	res := performAuthorized(api, http.MethodGet, "/me", nil)

	assertAPIError(t, res, http.StatusForbidden, "password_change_required")
	if sessions.revokedToken != "valid-token" {
		t.Fatalf("revoked token = %q, want valid-token", sessions.revokedToken)
	}
}

func TestTechnicianListsAssignedTenantJobsWithoutCommercialFields(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "technician", IsActive: true}}
	jobs := &fakeJobs{assigned: []*ent.Job{
		{ID: 1, CompanyID: &companyID, CustomerID: 44, JobType: "Repair", BillingType: "hourly", Subtasks: "[]"},
	}}
	api := New(testDependencies(users, jobs))
	res := performAuthorized(api, http.MethodGet, "/jobs", nil)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if jobs.assignedCompanyID != companyID || jobs.assignedUserID != 7 || jobs.listAllCalled {
		t.Fatalf("assigned company = %d, user = %d, list-all called = %v", jobs.assignedCompanyID, jobs.assignedUserID, jobs.listAllCalled)
	}
	body := res.Body.String()
	for _, forbidden := range []string{"customer_id", "billing_type", "line_items"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("technician response contains %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"job_type":"Repair"`) {
		t.Fatalf("assigned job missing: %s", body)
	}
}

func TestSubtaskPatchRequiresExplicitStateAndUpdatesAssignedJob(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Name: "Tech", Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, JobType: "Repair", Subtasks: `[{"title":"Inspect","completed":false,"sort_order":0}]`}}
	deps := testDependencies(users, jobs)
	deps.Policy = fakePolicy(true)
	mutations := &fakeMutations{subtask: services.APISubtaskResult{Title: "Inspect", Completed: true}}
	deps.Mutations = mutations
	api := New(deps)

	missing := performAuthorized(api, http.MethodPatch, "/jobs/4/subtasks/0", []byte(`{}`))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing completed status = %d, body = %s", missing.Code, missing.Body.String())
	}

	res := performAuthorized(api, http.MethodPatch, "/jobs/4/subtasks/0", []byte(`{"completed":true}`))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if mutations.subtaskCalls != 1 || mutations.subtaskActor.UserID != 7 || mutations.subtaskJobID != 4 || mutations.subtaskIndex != 0 || !mutations.subtaskCompleted {
		t.Fatalf("subtask mutation = %+v", mutations)
	}
}

func TestJSONDecoderRejectsUnknownFieldsAndOversizedBodies(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))

	unknown := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"email":"a","password":"b","extra":true}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownRes := httptest.NewRecorder()
	api.ServeHTTP(unknownRes, unknown)
	if unknownRes.Code != http.StatusBadRequest || !strings.Contains(unknownRes.Body.String(), "unknown field") {
		t.Fatalf("unknown field response = %d %s", unknownRes.Code, unknownRes.Body.String())
	}

	largeBody := `{"email":"a","password":"` + strings.Repeat("x", maxRequestBodyBytes) + `"}`
	large := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(largeBody))
	large.Header.Set("Content-Type", "application/json")
	largeRes := httptest.NewRecorder()
	api.ServeHTTP(largeRes, large)
	if largeRes.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized response = %d %s", largeRes.Code, largeRes.Body.String())
	}
}

func TestLoginCreatesBearerSession(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byEmail: &ent.User{ID: 7, CompanyID: &companyID, Name: "User", Email: "u@example.test", Role: "dispatcher", IsActive: true}}
	sessions := &fakeSessions{token: "new-token", userID: 7}
	deps := testDependencies(users, &fakeJobs{})
	deps.Sessions = sessions
	api := New(deps)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"email":"u@example.test","password":"secret","device_name":"  field phone  "}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	api.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if sessions.createdFor != 7 || sessions.deviceName != "field phone" || users.authEmail != "u@example.test" || users.completedID != 7 {
		t.Fatalf("createdFor=%d deviceName=%q authEmail=%q completedID=%d", sessions.createdFor, sessions.deviceName, users.authEmail, users.completedID)
	}
	var response struct {
		Data sessionDTO `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil || response.Data.Token != "new-token" {
		t.Fatalf("response = %s, err = %v", res.Body.String(), err)
	}
}

func TestLoginRejectsUserWhoMustChangePassword(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byEmail: &ent.User{ID: 7, CompanyID: &companyID, IsActive: true, ForcePasswordChange: true}}
	sessions := &fakeSessions{token: "new-token", userID: 7}
	deps := testDependencies(users, &fakeJobs{})
	deps.Sessions = sessions
	api := New(deps)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"email":"u@example.test","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(res, req)

	assertAPIError(t, res, http.StatusForbidden, "password_change_required")
	if sessions.createdFor != 0 || users.completedID != 0 {
		t.Fatalf("createdFor = %d, completedID = %d", sessions.createdFor, users.completedID)
	}
}

func TestRouterErrorsUseJSONEnvelope(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))

	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
	}{
		{name: "not found", method: http.MethodGet, path: "/missing", status: http.StatusNotFound, code: "not_found"},
		{name: "method not allowed", method: http.MethodPut, path: "/session", status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			api.ServeHTTP(res, httptest.NewRequest(tt.method, tt.path, nil))
			assertAPIError(t, res, tt.status, tt.code)
		})
	}
}

func TestLoginRateLimitDoesNotAffectAuthenticatedRoutes(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))

	for attempt := 1; attempt <= 6; attempt++ {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{}`))
		req.RemoteAddr = "192.0.2.10:12345"
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(res, req)

		want := http.StatusBadRequest
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if res.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, res.Code, want)
		}
		if attempt == 6 && res.Header().Get("Retry-After") == "" {
			t.Fatal("rate-limited response has no Retry-After header")
		}
		if attempt == 6 {
			assertAPIError(t, res, http.StatusTooManyRequests, "rate_limit_exceeded")
		}
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	api.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("authenticated route status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestLoginRateLimitRejectsSpoofedForwardedHeadersFromDirectClient(t *testing.T) {
	api := CaptureTransportPeer(chimw.RealIP(New(testDependencies(&fakeUsers{}, &fakeJobs{}))))

	for attempt := 1; attempt <= 6; attempt++ {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{}`))
		req.RemoteAddr = "192.0.2.10:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(attempt))
		api.ServeHTTP(res, req)

		if attempt < 6 && res.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want %d", attempt, res.Code, http.StatusBadRequest)
		}
		if attempt == 6 {
			assertAPIError(t, res, http.StatusTooManyRequests, "rate_limit_exceeded")
		}
	}
}

func TestLoginRateLimitDistinguishesClientsBehindLoopbackProxy(t *testing.T) {
	api := CaptureTransportPeer(chimw.RealIP(New(testDependencies(&fakeUsers{}, &fakeJobs{}))))

	request := func(clientIP string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", clientIP)
		api.ServeHTTP(res, req)
		return res
	}

	for attempt := 1; attempt <= 5; attempt++ {
		for _, clientIP := range []string{"198.51.100.10", "198.51.100.11"} {
			if res := request(clientIP); res.Code != http.StatusBadRequest {
				t.Fatalf("client %s attempt %d status = %d, want %d", clientIP, attempt, res.Code, http.StatusBadRequest)
			}
		}
	}
	assertAPIError(t, request("198.51.100.10"), http.StatusTooManyRequests, "rate_limit_exceeded")
}

func TestAPIPanicUsesJSONErrorEnvelope(t *testing.T) {
	deps := testDependencies(&fakeUsers{}, &fakeJobs{})
	deps.Users = nil
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"email":"tech@example.test","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")

	New(deps).ServeHTTP(res, req)

	assertAPIError(t, res, http.StatusInternalServerError, "internal_error")
}

func assertAPIError(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if res.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, status, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var response errorEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, res.Body.String())
	}
	if response.Error.Code != code {
		t.Fatalf("error code = %q, want %q", response.Error.Code, code)
	}
}

func testDependencies(users *fakeUsers, jobs *fakeJobs) Dependencies {
	return Dependencies{
		Sessions:    &fakeSessions{token: "valid-token", userID: 7},
		Users:       users,
		Jobs:        jobs,
		Statuses:    &fakeStatuses{},
		TimeEntries: &fakeTimeEntries{},
		Mutations:   &fakeMutations{},
		StatusFlow:  &fakeStatusFlow{},
		Policy:      fakePolicy(false),
	}
}

func performAuthorized(api http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	api.ServeHTTP(res, req)
	return res
}

type fakeSessions struct {
	token        string
	userID       int64
	createdFor   int64
	deviceName   string
	revokedToken string
}

func (f *fakeSessions) CreateMobile(_ context.Context, userID int64, deviceName string) (string, error) {
	f.createdFor = userID
	f.deviceName = deviceName
	return f.token, nil
}
func (f *fakeSessions) ValidateMobile(_ context.Context, token string) (int64, error) {
	if token != f.token {
		return 0, errors.New("invalid token")
	}
	return f.userID, nil
}
func (f *fakeSessions) RevokeMobile(_ context.Context, token string) error {
	f.revokedToken = token
	return nil
}

type fakeUsers struct {
	byID, byEmail *ent.User
	gotID         int64
	authEmail     string
	completedID   int64
}

func (f *fakeUsers) Authenticate(_ context.Context, email, _ string) error {
	f.authEmail = email
	return nil
}
func (f *fakeUsers) GetByEmail(context.Context, string) (*ent.User, error) {
	if f.byEmail == nil {
		return nil, errors.New("not found")
	}
	return f.byEmail, nil
}
func (f *fakeUsers) GetByID(_ context.Context, id int64) (*ent.User, error) {
	f.gotID = id
	if f.byID == nil {
		return nil, errors.New("not found")
	}
	return f.byID, nil
}
func (f *fakeUsers) CompleteOnboarding(_ context.Context, id int64) error {
	f.completedID = id
	return nil
}

type fakeJobs struct {
	all, assigned     []*ent.Job
	byID              *ent.Job
	listAllCalled     bool
	listCompanyID     int64
	assignedCompanyID int64
	assignedUserID    int64
	gotCompanyID      int64
	gotID             int64
}

func (f *fakeJobs) ListAllForCompany(_ context.Context, companyID int64) ([]*ent.Job, error) {
	f.listAllCalled = true
	f.listCompanyID = companyID
	return f.all, nil
}
func (f *fakeJobs) ListAssignedAllForCompany(_ context.Context, companyID, userID int64) ([]*ent.Job, error) {
	f.assignedCompanyID = companyID
	f.assignedUserID = userID
	return f.assigned, nil
}
func (f *fakeJobs) GetByIDForCompany(_ context.Context, companyID, id int64) (*ent.Job, error) {
	f.gotCompanyID = companyID
	f.gotID = id
	if f.byID == nil {
		return nil, errors.New("not found")
	}
	return f.byID, nil
}

type fakeStatuses struct {
	items      []*ent.Status
	err        error
	companyID  int64
	objectType string
}

func (f *fakeStatuses) ByObjectTypeForCompany(_ context.Context, companyID int64, objectType string) ([]*ent.Status, error) {
	f.companyID = companyID
	f.objectType = objectType
	return f.items, f.err
}

type fakeTimeEntries struct {
	active          *ent.TimeEntry
	activeErr       error
	activeCompanyID int64
	activeUserID    int64
}

func (f *fakeTimeEntries) GetActiveByUserForCompany(_ context.Context, companyID, userID int64) (*ent.TimeEntry, error) {
	f.activeCompanyID = companyID
	f.activeUserID = userID
	if f.activeErr != nil {
		return nil, f.activeErr
	}
	if f.active == nil {
		return nil, errors.New("not found")
	}
	return f.active, nil
}

type fakeMutations struct {
	subtask          services.APISubtaskResult
	subtaskErr       error
	subtaskCalls     int
	subtaskActor     services.MutationActor
	subtaskJobID     int64
	subtaskIndex     int
	subtaskCompleted bool
	clocked          services.APITimeEntryResult
	clockInErr       error
	clockInCalls     int
	clockInActor     services.MutationActor
	clockInJobID     int64
	clockInParams    services.APIClockInParams
	clockOutErr      error
	clockOutCalls    int
	clockOutActor    services.MutationActor
}

func (f *fakeMutations) SetSubtaskCompletion(_ context.Context, actor services.MutationActor, jobID int64, index int, completed bool) (services.APISubtaskResult, error) {
	f.subtaskCalls++
	f.subtaskActor = actor
	f.subtaskJobID = jobID
	f.subtaskIndex = index
	f.subtaskCompleted = completed
	return f.subtask, f.subtaskErr
}

func (f *fakeMutations) ClockIn(_ context.Context, actor services.MutationActor, jobID int64, params services.APIClockInParams) (services.APITimeEntryResult, error) {
	f.clockInCalls++
	f.clockInActor = actor
	f.clockInJobID = jobID
	f.clockInParams = params
	if f.clockInErr != nil {
		return services.APITimeEntryResult{}, f.clockInErr
	}
	if f.clocked.ClockIn.IsZero() {
		f.clocked.ClockIn = time.Now()
	}
	return f.clocked, nil
}

func (f *fakeMutations) ClockOut(_ context.Context, actor services.MutationActor) (services.APITimeEntryResult, error) {
	f.clockOutCalls++
	f.clockOutActor = actor
	if f.clockOutErr != nil {
		return services.APITimeEntryResult{}, f.clockOutErr
	}
	if f.clocked.ClockIn.IsZero() {
		f.clocked.ClockIn = time.Now()
	}
	return f.clocked, nil
}

type fakeStatusFlow struct {
	err      error
	calls    int
	actor    statusflow.Actor
	jobID    int64
	statusID int64
}

func (f *fakeStatusFlow) TransitionJob(_ context.Context, actor statusflow.Actor, jobID, statusID int64) error {
	f.calls++
	f.actor = actor
	f.jobID = jobID
	f.statusID = statusID
	return f.err
}

type fakePolicy bool

func (f fakePolicy) IsUserAssignedToJob(context.Context, int64, int64) bool { return bool(f) }
