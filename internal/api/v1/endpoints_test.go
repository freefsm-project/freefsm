package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestJobDetailUsesCompanyStatusSeamAndRedactsTechnicianCommercialFields(t *testing.T) {
	companyID, statusID := int64(12), int64(3)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, CustomerID: 44, StatusID: &statusID, JobType: "Repair", BillingType: "hourly", LineItems: `[{"description":"Secret","quantity":1,"unit_price":100}]`}}
	statuses := &fakeStatuses{items: []*ent.Status{{ID: statusID, Name: "Working", CategoryKey: string(statusflow.JobInProgress)}}}
	deps := testDependencies(users, jobs)
	deps.Statuses = statuses
	deps.Policy = fakePolicy(true)

	res := performAuthorized(New(deps), http.MethodGet, "/jobs/4", nil)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if jobs.gotCompanyID != companyID || jobs.gotID != 4 || statuses.companyID != companyID || statuses.objectType != "job" {
		t.Fatalf("job lookup = (%d, %d), status lookup = (%d, %q)", jobs.gotCompanyID, jobs.gotID, statuses.companyID, statuses.objectType)
	}
	body := res.Body.String()
	if !strings.Contains(body, `"category":"job:in_progress"`) {
		t.Fatalf("status detail missing: %s", body)
	}
	assertNoCommercialFields(t, body)
}

func TestStatusTransitionUsesActorAndRedactsTechnicianMutationResponse(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, CustomerID: 44, JobType: "Repair", BillingType: "hourly", LineItems: `[]`}}
	flow := &fakeStatusFlow{}
	deps := testDependencies(users, jobs)
	deps.Policy = fakePolicy(true)
	deps.StatusFlow = flow

	res := performAuthorized(New(deps), http.MethodPost, "/jobs/4/status", []byte(`{"status_id":9}`))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if flow.calls != 1 || flow.actor != (statusflow.Actor{ID: 7, CompanyID: companyID}) || flow.jobID != 4 || flow.statusID != 9 {
		t.Fatalf("transition calls=%d actor=%+v job=%d status=%d", flow.calls, flow.actor, flow.jobID, flow.statusID)
	}
	assertNoCommercialFields(t, res.Body.String())
}

func TestStatusTransitionMapsServiceError(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID}}
	flow := &fakeStatusFlow{err: statusflow.ErrInvalidTransition}
	deps := testDependencies(users, jobs)
	deps.Policy = fakePolicy(true)
	deps.StatusFlow = flow

	res := performAuthorized(New(deps), http.MethodPost, "/jobs/4/status", []byte(`{"status_id":9}`))

	assertAPIError(t, res, http.StatusConflict, "invalid_transition")
	if flow.calls != 1 {
		t.Fatalf("transition calls = %d, want 1", flow.calls)
	}
}

func TestUnassignedTechnicianCannotAccessOrMutateJob(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID}}
	flow := &fakeStatusFlow{}
	deps := testDependencies(users, jobs)
	deps.StatusFlow = flow
	api := New(deps)

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/jobs/4"},
		{http.MethodPost, "/jobs/4/status"},
		{http.MethodPost, "/jobs/4/clock-in"},
		{http.MethodPatch, "/jobs/4/subtasks/0"},
	} {
		res := performAuthorized(api, request.method, request.path, []byte(`{}`))
		assertAPIError(t, res, http.StatusForbidden, "forbidden")
	}
	if flow.calls != 0 {
		t.Fatalf("transition calls = %d, want 0", flow.calls)
	}
}

func TestCookieOnlyAuthenticationIsRejected(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "valid-token"})
	res := httptest.NewRecorder()

	api.ServeHTTP(res, req)

	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")
}

func TestLogoutRevokesBearerSession(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	sessions := &fakeSessions{token: "valid-token", userID: 7}
	deps := testDependencies(users, &fakeJobs{})
	deps.Sessions = sessions

	res := performAuthorized(New(deps), http.MethodDelete, "/session", nil)

	if res.Code != http.StatusNoContent || res.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if sessions.revokedToken != "valid-token" || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("revoked token = %q, cache control = %q", sessions.revokedToken, res.Header().Get("Cache-Control"))
	}
}

func TestActiveTimeEntryReturnsCurrentEntry(t *testing.T) {
	companyID, jobID := int64(12), int64(4)
	clockIn := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	times := &fakeTimeEntries{active: &ent.TimeEntry{ID: 8, CompanyID: &companyID, UserID: 7, JobID: &jobID, ClockIn: clockIn, Notes: "On site"}}
	deps := testDependencies(users, &fakeJobs{})
	deps.TimeEntries = times

	res := performAuthorized(New(deps), http.MethodGet, "/time-entries/active", nil)

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":8`) || !strings.Contains(res.Body.String(), `"notes":"On site"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if times.activeCompanyID != companyID || times.activeUserID != 7 {
		t.Fatalf("active company/user = %d/%d, want %d/7", times.activeCompanyID, times.activeUserID, companyID)
	}
}

func TestClockInAndOutUseServiceBehavior(t *testing.T) {
	companyID, jobID := int64(12), int64(4)
	clockIn := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: jobID, CompanyID: &companyID}}
	mutations := &fakeMutations{clocked: services.APITimeEntryResult{ID: 8, UserID: 7, JobID: &jobID, ClockIn: clockIn}}
	deps := testDependencies(users, jobs)
	deps.Policy = fakePolicy(true)
	deps.Mutations = mutations
	api := New(deps)

	clockInRes := performAuthorized(api, http.MethodPost, "/jobs/4/clock-in", []byte(`{"notes":"Arrived","latitude":51.5,"longitude":-0.1}`))
	if clockInRes.Code != http.StatusCreated {
		t.Fatalf("clock-in status = %d, body = %s", clockInRes.Code, clockInRes.Body.String())
	}
	if mutations.clockInActor != (services.MutationActor{CompanyID: 12, UserID: 7, Role: "tech"}) || mutations.clockInJobID != 4 || mutations.clockInParams.Notes != "Arrived" || mutations.clockInParams.Latitude == nil {
		t.Fatalf("clock-in actor = %+v, job = %d, params = %+v", mutations.clockInActor, mutations.clockInJobID, mutations.clockInParams)
	}

	clockOutRes := performAuthorized(api, http.MethodPost, "/time-entries/clock-out", nil)
	if clockOutRes.Code != http.StatusOK || mutations.clockOutCalls != 1 || mutations.clockOutActor.UserID != 7 {
		t.Fatalf("clock-out status = %d, calls = %d, actor = %+v, body = %s", clockOutRes.Code, mutations.clockOutCalls, mutations.clockOutActor, clockOutRes.Body.String())
	}
}

func TestClockOutMapsEntryNoLongerActive(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	mutations := &fakeMutations{clockOutErr: services.ErrTimeEntryNotActive}
	deps := testDependencies(users, &fakeJobs{})
	deps.Mutations = mutations

	res := performAuthorized(New(deps), http.MethodPost, "/time-entries/clock-out", nil)

	assertAPIError(t, res, http.StatusConflict, "not_clocked_in")
}

func TestMutationErrorsKeepStructuredAPIMappings(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Name: "Tech", Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, Subtasks: `[{"title":"Inspect","completed":false}]`}}
	mutations := &fakeMutations{clockInErr: services.ErrActiveTimeEntry, subtaskErr: services.ErrSubtaskNotFound}
	deps := testDependencies(users, jobs)
	deps.Policy = fakePolicy(true)
	deps.Mutations = mutations
	api := New(deps)

	clockIn := performAuthorized(api, http.MethodPost, "/jobs/4/clock-in", []byte(`{}`))
	assertAPIError(t, clockIn, http.StatusConflict, "already_clocked_in")
	subtask := performAuthorized(api, http.MethodPatch, "/jobs/4/subtasks/0", []byte(`{"completed":true}`))
	assertAPIError(t, subtask, http.StatusNotFound, "subtask_not_found")
}

func TestTransactionMutationErrorsKeepStructuredAPIMappings(t *testing.T) {
	companyID := int64(12)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "tech", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, Subtasks: `[{"title":"Inspect","completed":false}]`}}

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "forbidden", err: services.ErrMutationForbidden, wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "closed", err: services.ErrMutationJobClosed, wantStatus: http.StatusConflict, wantCode: "job_closed"},
		{name: "not found", err: services.ErrMutationJobNotFound, wantStatus: http.StatusNotFound, wantCode: "job_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutations := &fakeMutations{clockInErr: test.err, subtaskErr: test.err}
			deps := testDependencies(users, jobs)
			deps.Policy = fakePolicy(true)
			deps.Mutations = mutations
			api := New(deps)

			assertAPIError(t, performAuthorized(api, http.MethodPost, "/jobs/4/clock-in", []byte(`{}`)), test.wantStatus, test.wantCode)
			assertAPIError(t, performAuthorized(api, http.MethodPatch, "/jobs/4/subtasks/0", []byte(`{"completed":true}`)), test.wantStatus, test.wantCode)
		})
	}
}

func TestClosedAssignedJobRejectsTechnicianStatusAndWorkMutations(t *testing.T) {
	for _, role := range []string{"tech", "technician"} {
		t.Run(role, func(t *testing.T) {
			companyID, statusID := int64(12), int64(3)
			users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: role, IsActive: true}}
			jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, StatusID: &statusID, Subtasks: `[{"title":"Inspect","completed":false}]`}}
			statuses := &fakeStatuses{items: []*ent.Status{{ID: statusID, CategoryKey: string(statusflow.JobCompleted)}}}
			flow := &fakeStatusFlow{}
			mutations := &fakeMutations{}
			deps := testDependencies(users, jobs)
			deps.Policy = fakePolicy(true)
			deps.Statuses = statuses
			deps.StatusFlow = flow
			deps.Mutations = mutations
			api := New(deps)

			transition := performAuthorized(api, http.MethodPost, "/jobs/4/status", []byte(`{"status_id":9}`))
			assertAPIError(t, transition, http.StatusConflict, "job_closed")
			clockIn := performAuthorized(api, http.MethodPost, "/jobs/4/clock-in", []byte(`{}`))
			assertAPIError(t, clockIn, http.StatusConflict, "job_closed")
			subtask := performAuthorized(api, http.MethodPatch, "/jobs/4/subtasks/0", []byte(`{"completed":true}`))
			assertAPIError(t, subtask, http.StatusConflict, "job_closed")
			if flow.calls != 0 || mutations.clockInCalls != 0 || mutations.subtaskCalls != 0 {
				t.Fatalf("transition calls = %d, clock-in calls = %d, subtask calls = %d", flow.calls, mutations.clockInCalls, mutations.subtaskCalls)
			}
		})
	}
}

func TestOfficeRoleCanReopenClosedJobThroughStatusTransition(t *testing.T) {
	companyID, statusID := int64(12), int64(3)
	users := &fakeUsers{byID: &ent.User{ID: 7, CompanyID: &companyID, Role: "dispatcher", IsActive: true}}
	jobs := &fakeJobs{byID: &ent.Job{ID: 4, CompanyID: &companyID, StatusID: &statusID}}
	statuses := &fakeStatuses{items: []*ent.Status{{ID: statusID, CategoryKey: string(statusflow.JobCompleted)}}}
	flow := &fakeStatusFlow{}
	deps := testDependencies(users, jobs)
	deps.Statuses = statuses
	deps.StatusFlow = flow

	res := performAuthorized(New(deps), http.MethodPost, "/jobs/4/status", []byte(`{"status_id":9}`))

	if res.Code != http.StatusOK || flow.calls != 1 || flow.statusID != 9 {
		t.Fatalf("status = %d, calls = %d, target status = %d, body = %s", res.Code, flow.calls, flow.statusID, res.Body.String())
	}
}

func assertNoCommercialFields(t *testing.T, body string) {
	t.Helper()
	for _, field := range []string{"customer_id", "billing_type", "line_items", "Secret"} {
		if strings.Contains(body, field) {
			t.Fatalf("technician response contains %q: %s", field, body)
		}
	}
}
