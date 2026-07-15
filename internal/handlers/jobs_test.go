package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestJobPostRoutesRejectStaleFormIdentityBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		path string
		form url.Values
	}{
		{name: "create missing markers", path: "/jobs", form: validJobForm()},
		{name: "create carrying edit markers", path: "/jobs", form: withJobMarkers(validJobForm(), "edit", "41")},
		{name: "edit missing markers", path: "/jobs/41", form: validJobForm()},
		{name: "edit carrying create markers", path: "/jobs/41", form: withJobMarkers(validJobForm(), "create", "0")},
		{name: "edit carrying another job ID", path: "/jobs/41", form: withJobMarkers(validJobForm(), "edit", "42")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutations := 0
			h := &JobHandler{
				createJob: func(context.Context, int64, services.JobCreateParams) (*ent.Job, error) {
					mutations++
					return &ent.Job{ID: 1}, nil
				},
				updateJob: func(context.Context, int64, int64, services.JobUpdateParams) (*ent.Job, error) {
					mutations++
					return &ent.Job{ID: 41}, nil
				},
			}

			router := jobMutationTestRouter(h)
			req := authenticatedJobRequest(http.MethodPost, tt.path, tt.form)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusConflict, rr.Body.String())
			}
			if mutations != 0 {
				t.Fatalf("service mutations = %d, want 0", mutations)
			}
			if !strings.Contains(strings.ToLower(rr.Body.String()), "reload") {
				t.Fatalf("response should give a generic reload instruction, got %q", rr.Body.String())
			}
		})
	}
}

func TestJobPostRoutesAcceptCanonicalFormIdentity(t *testing.T) {
	var createdCompanyID int64
	var updatedID int64
	h := &JobHandler{
		getJobForCompany: func(context.Context, int64, int64) (*ent.Job, error) {
			return &ent.Job{ID: 41}, nil
		},
		createJob: func(_ context.Context, companyID int64, params services.JobCreateParams) (*ent.Job, error) {
			createdCompanyID = companyID
			return &ent.Job{ID: 80, JobType: params.JobType}, nil
		},
		updateJob: func(_ context.Context, companyID, id int64, params services.JobUpdateParams) (*ent.Job, error) {
			if companyID != 7 {
				t.Fatalf("update company ID = %d, want 7", companyID)
			}
			updatedID = id
			return &ent.Job{ID: id, JobType: *params.JobType}, nil
		},
	}
	router := jobMutationTestRouter(h)

	createReq := authenticatedJobRequest(http.MethodPost, "/jobs", withJobMarkers(validJobForm(), "create", "0"))
	createRR := httptest.NewRecorder()
	router.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want %d; body=%q", createRR.Code, http.StatusSeeOther, createRR.Body.String())
	}
	if createdCompanyID != 7 {
		t.Fatalf("create company ID = %d, want authenticated company 7", createdCompanyID)
	}

	updateForm := withJobMarkers(validJobForm(), "edit", "41")
	updateForm.Set("status_id", "999")
	updateReq := authenticatedJobRequest(http.MethodPost, "/jobs/41", updateForm)
	updateRR := httptest.NewRecorder()
	router.ServeHTTP(updateRR, updateReq)
	if updateRR.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want %d; body=%q", updateRR.Code, http.StatusSeeOther, updateRR.Body.String())
	}
	if updatedID != 41 {
		t.Fatalf("updated ID = %d, want 41", updatedID)
	}
}

func TestJobHandlersAuthenticateBeforeParsingOrClassifyingForm(t *testing.T) {
	tests := []struct {
		name string
		path string
		body io.Reader
	}{
		{name: "create malformed body", path: "/jobs", body: errorReader{}},
		{name: "create stale markers", path: "/jobs", body: strings.NewReader(validJobForm().Encode())},
		{name: "update malformed body", path: "/jobs/41", body: errorReader{}},
		{name: "update stale markers", path: "/jobs/41", body: strings.NewReader(validJobForm().Encode())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, tt.body)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			jobMutationTestRouter(&JobHandler{}).ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want authentication failure %d; body=%q", rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
		})
	}
}

func TestJobFormGETsDisableCaching(t *testing.T) {
	h := &JobHandler{
		newJobFormData: func(context.Context, int64) templates.JobFormPageData {
			return templates.JobFormPageData{Job: &templates.JobDetail{}, Errors: map[string]string{}}
		},
		getJobForCompany: func(context.Context, int64, int64) (*ent.Job, error) {
			return &ent.Job{ID: 41}, nil
		},
		editJobFormData: func(context.Context, *ent.Job) templates.JobFormPageData {
			return templates.JobFormPageData{Job: &templates.JobDetail{ID: 41}, Errors: map[string]string{}}
		},
	}

	for _, path := range []string{"/jobs/new", "/jobs/41/edit"} {
		req := authenticatedJobRequest(http.MethodGet, path, url.Values{})
		rr := httptest.NewRecorder()
		jobMutationTestRouter(h).ServeHTTP(rr, req)
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestJobEditGETLoadsWithinAuthenticatedCompany(t *testing.T) {
	var gotCompanyID, gotJobID int64
	h := &JobHandler{getJobForCompany: func(_ context.Context, companyID, jobID int64) (*ent.Job, error) {
		gotCompanyID, gotJobID = companyID, jobID
		return nil, errors.New("not found")
	}}
	req := authenticatedJobRequest(http.MethodGet, "/jobs/41/edit", url.Values{})
	rr := httptest.NewRecorder()
	jobMutationTestRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if gotCompanyID != 7 || gotJobID != 41 {
		t.Fatalf("scoped lookup arguments = company %d, job %d; want company 7, job 41", gotCompanyID, gotJobID)
	}
}

func TestForeignJobUpdateDoesNotCallMutation(t *testing.T) {
	mutations := 0
	h := &JobHandler{
		getJobForCompany: func(context.Context, int64, int64) (*ent.Job, error) {
			return nil, errors.New("not found")
		},
		updateJob: func(context.Context, int64, int64, services.JobUpdateParams) (*ent.Job, error) {
			mutations++
			return nil, nil
		},
	}
	req := authenticatedJobRequest(http.MethodPost, "/jobs/41", withJobMarkers(validJobForm(), "edit", "41"))
	rr := httptest.NewRecorder()
	jobMutationTestRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if mutations != 0 {
		t.Fatalf("update calls = %d, want 0", mutations)
	}
}

func TestJobMutationsMapExpectedValidationErrorToSafeBadRequest(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	tests := []struct {
		name string
		path string
		form url.Values
	}{
		{name: "create", path: "/jobs", form: withJobMarkers(validJobForm(), "create", "0")},
		{name: "update", path: "/jobs/41", form: withJobMarkers(validJobForm(), "edit", "41")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			h := &JobHandler{
				getJobForCompany: func(context.Context, int64, int64) (*ent.Job, error) {
					return &ent.Job{ID: 41}, nil
				},
				createJob: func(context.Context, int64, services.JobCreateParams) (*ent.Job, error) {
					return nil, fmt.Errorf("private relation detail: %w", services.NewJobInputError(services.JobInputReasonOwnershipMismatch, services.JobInputRelationProject))
				},
				updateJob: func(context.Context, int64, int64, services.JobUpdateParams) (*ent.Job, error) {
					return nil, fmt.Errorf("private relation detail: %w", services.NewJobInputError(services.JobInputReasonOwnershipMismatch, services.JobInputRelationProject))
				},
			}
			req := authenticatedJobRequest(http.MethodPost, tt.path, tt.form)
			rr := httptest.NewRecorder()
			jobMutationTestRouter(h).ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
			if got := rr.Body.String(); got != "Invalid job details\n" {
				t.Fatalf("body = %q, want generic validation response", got)
			}
			if strings.Contains(rr.Body.String(), "private relation detail") {
				t.Fatalf("response exposed service details: %q", rr.Body.String())
			}
			logOutput := logs.String()
			for _, want := range []string{`"authenticated_company_id":7`, `"reason":"ownership_mismatch"`, `"relation":"project"`, `"operation":"` + tt.name + `"`} {
				if !strings.Contains(logOutput, want) {
					t.Errorf("structured log missing %s: %s", want, logOutput)
				}
			}
			if strings.Contains(logOutput, "private relation detail") {
				t.Fatalf("structured log exposed wrapped internal details: %s", logOutput)
			}
		})
	}
}

func TestInvalidJobInputLogBoundsRequestControlledContext(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	oversized := strings.Repeat("x", jobLogValueMaxBytes*4)
	handler := chimiddleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleInvalidJobInput(w, r, "create", services.NewJobInputError(services.JobInputReasonOwnershipMismatch, services.JobInputRelationProject))
	}))
	req := authenticatedJobRequest(http.MethodPost, "/jobs/"+oversized, url.Values{})
	req.Header.Set("X-Request-ID", oversized)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest || rr.Body.String() != "Invalid job details\n" {
		t.Fatalf("response status=%d body=%q, want generic 400", rr.Code, rr.Body.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("decode structured log: %v", err)
	}
	for _, key := range []string{"request_id", "route"} {
		value, ok := entry[key].(string)
		if !ok {
			t.Fatalf("%s log value = %#v, want string", key, entry[key])
		}
		if len(value) > jobLogValueMaxBytes {
			t.Errorf("%s log length = %d, want <= %d", key, len(value), jobLogValueMaxBytes)
		}
	}
	if entry["request_id"] != oversized[:jobLogValueMaxBytes] {
		t.Fatalf("request_id = %#v, want capped request header", entry["request_id"])
	}
	wantRoute := ("/jobs/" + oversized)[:jobLogValueMaxBytes]
	if entry["route"] != wantRoute {
		t.Fatalf("route = %#v, want %q", entry["route"], wantRoute)
	}
	if entry["reason"] != string(services.JobInputReasonOwnershipMismatch) || entry["relation"] != string(services.JobInputRelationProject) {
		t.Fatalf("diagnostic fields = reason %#v relation %#v", entry["reason"], entry["relation"])
	}
	if strings.Contains(logs.String(), oversized) {
		t.Fatal("structured log contains unbounded request-controlled value")
	}
}

func TestJobCreateRequiresPositiveAuthenticatedCompanyBeforeMutation(t *testing.T) {
	mutations := 0
	h := &JobHandler{createJob: func(context.Context, int64, services.JobCreateParams) (*ent.Job, error) {
		mutations++
		return &ent.Job{ID: 80}, nil
	}}
	req := authenticatedJobRequest(http.MethodPost, "/jobs", withJobMarkers(validJobForm(), "create", "0"))
	user, _ := middleware.UserFromContext(req.Context())
	user.CompanyID = 0
	rr := httptest.NewRecorder()
	jobMutationTestRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if mutations != 0 {
		t.Fatalf("service mutations = %d, want 0", mutations)
	}
}

func TestJobStatusConfigurationErrorReturnsGeneric503AndLogsSafeContext(t *testing.T) {
	oldVersion, oldCommit := config.Version, config.Commit
	config.Version, config.Commit = "test-version", "test-commit"
	t.Cleanup(func() {
		config.Version, config.Commit = oldVersion, oldCommit
	})

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	statusErr := &services.StatusConfigurationError{
		CompanyID:  7,
		ObjectType: "job",
		Category:   "job:new",
	}
	h := &JobHandler{createJob: func(context.Context, int64, services.JobCreateParams) (*ent.Job, error) {
		return nil, fmt.Errorf("private ent detail: %w", statusErr)
	}}
	handler := chimiddleware.RequestID(jobMutationTestRouter(h))
	req := authenticatedJobRequest(http.MethodPost, "/jobs", withJobMarkers(validJobForm(), "create", "0"))
	req.Header.Set("X-Request-ID", "request-123")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if got := rr.Body.String(); got != "Service temporarily unavailable\n" {
		t.Fatalf("body = %q, want generic unavailable response", got)
	}
	logOutput := logs.String()
	for _, want := range []string{
		`"request_id":"request-123"`,
		`"version":"test-version"`,
		`"commit":"test-commit"`,
		`"route":"/jobs"`,
		`"operation":"create"`,
		`"submitted_customer_id":12`,
		`"authenticated_company_id":7`,
		`"error_company_id":7`,
		`"error_object_type":"job"`,
		`"error_category":"job:new"`,
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("structured log missing %s: %s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, "private ent detail") {
		t.Fatalf("structured log exposed wrapped internal details: %s", logOutput)
	}
}

func TestCreateNextOccurrencePassesCompanyAndMapsStatusConfigurationError(t *testing.T) {
	tests := []struct {
		name       string
		result     *ent.Job
		err        error
		wantStatus int
	}{
		{name: "success", result: &ent.Job{ID: 82, JobType: "Repair"}, wantStatus: http.StatusSeeOther},
		{name: "status configuration unavailable", err: &services.StatusConfigurationError{CompanyID: 7, ObjectType: "job", Category: "job:new"}, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotCompanyID, gotSourceID int64
			h := &JobHandler{
				canUpdateJob: func(context.Context, int64, string, int64) bool { return true },
				createNext: func(_ context.Context, companyID, sourceID int64, _ time.Time) (*ent.Job, error) {
					gotCompanyID, gotSourceID = companyID, sourceID
					return tt.result, tt.err
				},
			}
			router := chi.NewRouter()
			router.Post("/jobs/{id}/create-next-occurrence", h.CreateNextOccurrence)
			req := authenticatedJobRequest(http.MethodPost, "/jobs/41/create-next-occurrence", url.Values{})
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if gotCompanyID != 7 || gotSourceID != 41 {
				t.Fatalf("CreateNextOccurrence arguments = company %d, source %d; want company 7, source 41", gotCompanyID, gotSourceID)
			}
			if tt.wantStatus == http.StatusServiceUnavailable && rr.Body.String() != "Service temporarily unavailable\n" {
				t.Fatalf("body = %q, want generic unavailable response", rr.Body.String())
			}
		})
	}
}

func jobMutationTestRouter(h *JobHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/jobs/new", h.Create)
	r.Post("/jobs", h.Create)
	r.Get("/jobs/{id}/edit", h.Update)
	r.Post("/jobs/{id}", h.Update)
	return r
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func authenticatedJobRequest(method, path string, form url.Values) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserKey, &middleware.UserInfo{
		ID: 5, CompanyID: 7, Name: "Dispatcher", Role: "dispatcher",
	})
	return req.WithContext(ctx)
}

func validJobForm() url.Values {
	return url.Values{
		"customer_id":  {"12"},
		"job_type":     {"Repair"},
		"billing_type": {"flat_rate"},
	}
}

func withJobMarkers(form url.Values, mode, id string) url.Values {
	cloned := make(url.Values, len(form)+2)
	for key, values := range form {
		cloned[key] = append([]string(nil), values...)
	}
	cloned.Set("form_mode", mode)
	cloned.Set("job_id", id)
	return cloned
}
