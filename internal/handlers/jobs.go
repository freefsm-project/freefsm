package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type JobHandler struct {
	svc              *services.JobService
	custSvc          *services.CustomerService
	statusSvc        *services.StatusService
	projectSvc       *services.ProjectService
	locSvc           *services.LocationService
	contactSvc       *services.CustomerContactService
	tagSvc           *services.TagService
	tagLinkSvc       *services.TagLinkService
	defSvc           *services.CustomFieldDefinitionService
	assetSvc         *services.AssetService
	assetTypeSvc     *services.AssetTypeService
	assetStatSvc     *services.AssetStatusService
	fileSvc          *services.FileService
	activitySvc      *services.ActivityService
	userSvc          *services.UserService
	policySvc        *services.PolicyService
	timeEntrySvc     *services.TimeEntryService
	statusflow       *statusflow.Service
	createJob        func(context.Context, int64, services.JobCreateParams) (*ent.Job, error)
	updateJob        func(context.Context, int64, int64, services.JobUpdateParams) (*ent.Job, error)
	createNext       func(context.Context, int64, int64, time.Time) (*ent.Job, error)
	canUpdateJob     func(context.Context, int64, string, int64) bool
	getJobForCompany func(context.Context, int64, int64) (*ent.Job, error)
	newJobFormData   func(context.Context, int64, int64) templates.JobFormPageData
	editJobFormData  func(context.Context, int64, *ent.Job) (templates.JobFormPageData, error)
}

func NewJobHandler(svc *services.JobService, custSvc *services.CustomerService, statusSvc *services.StatusService, projectSvc *services.ProjectService, locSvc *services.LocationService, contactSvc *services.CustomerContactService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, assetSvc *services.AssetService, assetTypeSvc *services.AssetTypeService, assetStatSvc *services.AssetStatusService, fileSvc *services.FileService, activitySvc *services.ActivityService, userSvc *services.UserService, policySvc *services.PolicyService, timeEntrySvc *services.TimeEntryService) *JobHandler {
	h := &JobHandler{svc: svc, custSvc: custSvc, statusSvc: statusSvc, projectSvc: projectSvc, locSvc: locSvc, contactSvc: contactSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, assetSvc: assetSvc, assetTypeSvc: assetTypeSvc, assetStatSvc: assetStatSvc, fileSvc: fileSvc, activitySvc: activitySvc, userSvc: userSvc, policySvc: policySvc, timeEntrySvc: timeEntrySvc, createJob: svc.Create, updateJob: svc.Update, createNext: svc.CreateNextOccurrence, getJobForCompany: svc.GetByIDForCompany, canUpdateJob: func(ctx context.Context, userID int64, role string, jobID int64) bool {
		return policySvc.CanAccessObject(ctx, userID, role, objectref.New(objectref.TypeJob, jobID), policyUpdate)
	}}
	h.newJobFormData = h.newJobForm
	h.editJobFormData = func(ctx context.Context, companyID int64, job *ent.Job) (templates.JobFormPageData, error) {
		return h.formDataFromJob(ctx, companyID, job, h.statusesForSelect(ctx))
	}
	return h
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)

	u, _ := middleware.UserFromContext(r.Context())
	var jobs []*ent.Job
	var total int
	var err error
	if isAdminOrDispatcher(u) && customerID > 0 {
		jobs, total, err = h.svc.ListForCustomer(r.Context(), customerID, search, statusID, page, perPage)
	} else if isAdminOrDispatcher(u) {
		jobs, total, err = h.svc.List(r.Context(), search, statusID, page, perPage)
	} else if u != nil {
		jobs, total, err = h.svc.ListAssigned(r.Context(), u.ID, search, statusID, page, perPage)
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.JobRow, len(jobs))
	for i, j := range jobs {
		rows[i] = jobRow(r.Context(), j, statuses, custMap)
	}

	data := templates.JobListPageData{
		Jobs:       rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.JobPaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		CustomerID: customerID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.JobsTable(data).Render(r.Context(), w)
		return
	}
	templates.JobsIndex(data).Render(r.Context(), w)
}

func (h *JobHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	d := jobToDetail(r.Context(), j, statuses)
	d.NextOccurrenceStart = time.Now().In(middleware.CompanyLocation(r.Context())).Format("2006-01-02T15:04")
	if j.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), j.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(j)
	d.Visits = services.ParseVisits(j.Visits)
	assignments, _ := h.svc.Assignments(r.Context(), j.ID)
	if len(assignments) == 0 {
		assignments = services.ParseAssignments(j.Assignments)
	}
	d.Assignments = assignments
	d.Subtasks = services.ParseSubtasks(j.Subtasks)
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeJob, j.ID))
	var allTags []*ent.Tag
	if isAdminOrDispatcher(u) {
		allTags, _ = h.tagSvc.ListAll(r.Context(), u.CompanyID)
	}
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	if j.ProjectID != nil && *j.ProjectID > 0 {
		project, _ := h.projectSvc.GetByID(r.Context(), *j.ProjectID)
		if project != nil && project.DeletedAt == nil {
			d.ProjectName = project.Name
		}
	}
	if j.LocationID != nil && *j.LocationID > 0 {
		location, _ := h.locSvc.GetByID(r.Context(), *j.LocationID)
		if location != nil {
			d.LocationName = location.Title
			d.LocationAddress = services.LocationAddress(location)
		}
	}
	if j.CustomerContactID != nil && *j.CustomerContactID > 0 {
		contact, _ := h.contactSvc.GetByID(r.Context(), *j.CustomerContactID)
		if contact != nil && contact.CustomerID == j.CustomerID {
			d.ContactName = contact.FirstName + " " + contact.LastName
		}
	}
	if j.AssetID != nil && *j.AssetID > 0 {
		asset, _ := h.assetSvc.GetByID(r.Context(), *j.AssetID)
		if asset != nil {
			d.AssetName = assetLabel(asset)
		}
	}
	activeEntry, err := h.timeEntrySvc.GetActiveByUser(r.Context(), u.ID)
	if err == nil && activeEntry != nil {
		if activeEntry.JobID != nil && *activeEntry.JobID == j.ID {
			d.ActiveTimeEntryOnJob = true
			d.ActiveTimeEntryClockIn = displayDateTime(r.Context(), activeEntry.ClockIn)
			d.ActiveTimeEntryDuration = services.TimeEntryDuration(activeEntry.ClockIn, safeTime(activeEntry.ClockOut))
		} else {
			d.ActiveTimeEntryElsewhere = true
			if activeEntry.JobID != nil {
				activeJob, _ := h.svc.GetByID(r.Context(), *activeEntry.JobID)
				if activeJob != nil {
					d.ActiveTimeEntryMessage = "You are already clocked in to " + jobDisplayName(activeJob) + ". Clock out before clocking in to this job."
				}
			}
			if d.ActiveTimeEntryMessage == "" {
				d.ActiveTimeEntryMessage = "You are already clocked in. Clock out before clocking in to this job."
			}
		}
	}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "job")
	d.CustomFields = buildCustomFieldDisplay(defs, j.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeJob, j.ID))
	d.FileList = templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: j.ID, ObjectType: "job"}
	ctx := middleware.WithPageHeaderTitle(r.Context(), j.JobType)
	templates.JobShow(d).Render(ctx, w)
}

func (h *JobHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyUpdate) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil || j.DeletedAt != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	if h.statusflow == nil {
		http.Error(w, "status workflow unavailable", http.StatusInternalServerError)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	err = h.statusflow.TransitionJob(r.Context(), statusflow.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, id, statusID)
	if err != nil {
		statusflowHTTPError(w, err)
		return
	}
	result, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		internalServerError(w, r, "reload job status", err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		d := jobToDetail(r.Context(), result, statuses)
		render(w, r, templates.JobStatusControl(d))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=Status+updated", id), http.StatusSeeOther)
}

func (h *JobHandler) ClockIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	result, err := h.timeEntrySvc.ClockIn(r.Context(), services.TimeEntryCreateParams{UserID: u.ID, JobID: id})
	if err != nil {
		if errors.Is(err, services.ErrActiveTimeEntry) {
			redirectToJob(w, r, id, "You are already clocked in. Clock out before clocking in to this job.")
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	clockInStr := displayDateTime(r.Context(), result.ClockIn)
	jobName := jobDisplayName(j)
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "clocked_in", objectref.New(objectref.TypeTimeEntry, result.ID), map[string]interface{}{
		"entity_name":   fmt.Sprintf("%s — %s — %s", u.Name, jobName, clockInStr),
		"actor_name":    u.Name,
		"time_entry_id": result.ID,
		"clock_in":      clockInStr,
	})
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "clocked_in", objectref.New(objectref.TypeJob, id), map[string]interface{}{
		"entity_name":   fmt.Sprintf("%s — %s", jobName, clockInStr),
		"actor_name":    u.Name,
		"time_entry_id": result.ID,
		"clock_in":      clockInStr,
	})
	redirectToJob(w, r, id, "Clocked in to job")
}

func (h *JobHandler) ClockOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	activeEntry, err := h.timeEntrySvc.GetActiveByUser(r.Context(), u.ID)
	if err != nil {
		redirectToJob(w, r, id, "No active clock-in found for this job")
		return
	}
	if activeEntry.JobID == nil {
		redirectToJob(w, r, id, "Your active clock-in is not linked to this job")
		return
	}
	if *activeEntry.JobID != id {
		redirectToJob(w, r, id, "You are clocked in to another job")
		return
	}
	result, err := h.timeEntrySvc.ClockOut(r.Context(), activeEntry.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	duration := services.TimeEntryDuration(result.ClockIn, safeTime(result.ClockOut))
	clockInStr := displayDateTime(r.Context(), result.ClockIn)
	jobName := jobDisplayName(j)
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "clocked_out", objectref.New(objectref.TypeTimeEntry, result.ID), map[string]interface{}{
		"entity_name":   fmt.Sprintf("%s — %s — %s (%s)", u.Name, jobName, clockInStr, duration),
		"actor_name":    u.Name,
		"time_entry_id": result.ID,
		"clock_in":      clockInStr,
		"duration":      duration,
	})
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "clocked_out", objectref.New(objectref.TypeJob, id), map[string]interface{}{
		"entity_name":   fmt.Sprintf("%s — %s (%s)", jobName, clockInStr, duration),
		"actor_name":    u.Name,
		"time_entry_id": result.ID,
		"clock_in":      clockInStr,
		"duration":      duration,
	})
	redirectToJob(w, r, id, "Clocked out of job")
}

func redirectToJob(w http.ResponseWriter, r *http.Request, id int64, flash string) {
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=%s", id, url.QueryEscape(flash)), http.StatusSeeOther)
}

func validJobFormIdentity(r *http.Request, mode string, jobID int64) bool {
	modes := r.Form["form_mode"]
	jobIDs := r.Form["job_id"]
	if len(modes) != 1 || modes[0] != mode || len(jobIDs) != 1 {
		return false
	}
	return jobIDs[0] == strconv.FormatInt(jobID, 10)
}

func staleJobForm(w http.ResponseWriter) {
	http.Error(w, "This form is out of date. Reload the page and try again.", http.StatusConflict)
}

const jobLogValueMaxBytes = 256

func boundedJobLogValue(value string) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= jobLogValueMaxBytes {
		return value
	}
	value = value[:jobLogValueMaxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func handleInvalidJobInput(w http.ResponseWriter, r *http.Request, operation string, err error) bool {
	var inputErr *services.JobInputError
	if !errors.As(err, &inputErr) {
		return false
	}
	var companyID int64
	if u, ok := middleware.UserFromContext(r.Context()); ok && u != nil {
		companyID = u.CompanyID
	}
	slog.Warn("invalid job input",
		"request_id", boundedJobLogValue(chimiddleware.GetReqID(r.Context())),
		"route", boundedJobLogValue(r.URL.Path),
		"operation", operation,
		"authenticated_company_id", companyID,
		"reason", inputErr.Reason(),
		"relation", inputErr.Relation(),
	)
	http.Error(w, "Invalid job details", http.StatusBadRequest)
	return true
}

func handleJobStatusConfigurationError(w http.ResponseWriter, r *http.Request, operation string, submittedCustomerID, authenticatedCompanyID int64, err error) bool {
	var configErr *services.StatusConfigurationError
	if !errors.As(err, &configErr) {
		return false
	}

	slog.Error("job status configuration unavailable",
		"request_id", boundedJobLogValue(chimiddleware.GetReqID(r.Context())),
		"version", config.Version,
		"commit", config.Commit,
		"route", boundedJobLogValue(r.URL.Path),
		"operation", operation,
		"submitted_customer_id", submittedCustomerID,
		"authenticated_company_id", authenticatedCompanyID,
		"error_company_id", configErr.CompanyID,
		"error_object_type", configErr.ObjectType,
		"error_category", configErr.Category,
	)
	http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
	return true
}

func (h *JobHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		newJobFormData := h.newJobFormData
		if newJobFormData == nil {
			newJobFormData = h.newJobForm
		}
		templates.JobForm(newJobFormData(r.Context(), u.CompanyID, customerID)).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if !validJobFormIdentity(r, "create", 0) {
		staleJobForm(w)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("customer_contact_id"), 10, 64)
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	loc := middleware.CompanyLocation(r.Context())
	params := services.JobCreateParams{
		CustomerID:        custID,
		ProjectID:         projectID,
		LocationID:        locationID,
		CustomerContactID: contactID,
		AssetID:           assetID,
		JobType:           r.FormValue("job_type"),
		Subtitle:          r.FormValue("subtitle"),
		StatusID:          0,
		BillingType:       r.FormValue("billing_type"),
		StartTime:         parseTime(r.FormValue("start_time"), loc),
		EndTime:           parseTime(r.FormValue("end_time"), loc),
		DueDate:           parseDate(r.FormValue("due_date"), loc),
		Notes:             r.FormValue("notes"),
		TechNotes:         r.FormValue("tech_notes"),
		Visits:            services.ParseVisits(r.FormValue("visits")),
		Assignments:       services.ParseAssignments(r.FormValue("assignments")),
		Subtasks:          services.ParseSubtasks(r.FormValue("subtasks")),
		CustomFields:      parseCustomFieldValues(r),
	}
	if params.BillingType == "" {
		params.BillingType = "flat_rate"
	}
	createJob := h.createJob
	if createJob == nil {
		createJob = h.svc.Create
	}
	result, err := createJob(r.Context(), u.CompanyID, params)
	if err != nil {
		if handleJobStatusConfigurationError(w, r, "create", custID, u.CompanyID, err) {
			return
		}
		if handleInvalidJobInput(w, r, "create", err) {
			return
		}
		internalServerError(w, r, "create job", err)
		return
	}
	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeJob, result.ID), map[string]interface{}{
			"entity_name": result.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs?flash=Job+created", http.StatusSeeOther)
}

func (h *JobHandler) CreateNextOccurrence(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	canUpdateJob := h.canUpdateJob
	if canUpdateJob == nil {
		canUpdateJob = func(ctx context.Context, userID int64, role string, jobID int64) bool {
			return h.policySvc.CanAccessObject(ctx, userID, role, objectref.New(objectref.TypeJob, jobID), policyUpdate)
		}
	}
	if !canUpdateJob(r.Context(), u.ID, u.Role, id) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	loc := middleware.CompanyLocation(r.Context())
	now := time.Now().In(loc)
	nextStart := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, loc)
	createNext := h.createNext
	if createNext == nil {
		createNext = h.svc.CreateNextOccurrence
	}
	result, err := createNext(r.Context(), u.CompanyID, id, nextStart)
	if err != nil {
		if handleJobStatusConfigurationError(w, r, "create_next_occurrence", 0, u.CompanyID, err) {
			return
		}
		if handleInvalidJobInput(w, r, "create_next_occurrence", err) {
			return
		}
		internalServerError(w, r, "create next occurrence", err)
		return
	}
	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created_next_occurrence", objectref.New(objectref.TypeJob, result.ID), map[string]interface{}{
			"entity_name":   result.JobType,
			"actor_name":    u.Name,
			"source_job_id": id,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d/edit?pending_next_occurrence=1&flash=Next+occurrence+created", result.ID), http.StatusSeeOther)
}

func (h *JobHandler) CancelNextOccurrence(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyUpdate) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		internalServerError(w, r, "cancel next occurrence", err)
		return
	}
	http.Redirect(w, r, "/jobs?flash=Next+occurrence+cancelled", http.StatusSeeOther)
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		getJobForCompany := h.getJobForCompany
		if getJobForCompany == nil {
			getJobForCompany = h.svc.GetByIDForCompany
		}
		j, err := getJobForCompany(r.Context(), u.CompanyID, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		editJobFormData := h.editJobFormData
		if editJobFormData == nil {
			editJobFormData = func(ctx context.Context, companyID int64, job *ent.Job) (templates.JobFormPageData, error) {
				return h.formDataFromJob(ctx, companyID, job, h.statusesForSelect(ctx))
			}
		}
		fd, err := editJobFormData(r.Context(), u.CompanyID, j)
		if err != nil {
			internalServerError(w, r, "build job edit form", err)
			return
		}
		fd.PendingNextOccurrence = r.URL.Query().Get("pending_next_occurrence") == "1"
		templates.JobForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if !validJobFormIdentity(r, "edit", id) {
		staleJobForm(w)
		return
	}
	getJobForCompany := h.getJobForCompany
	if getJobForCompany == nil {
		getJobForCompany = h.svc.GetByIDForCompany
	}
	if _, err := getJobForCompany(r.Context(), u.CompanyID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("customer_contact_id"), 10, 64)
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	params := services.JobUpdateParams{
		CustomerID:        int64Ptr(custID),
		ProjectID:         &projectID,
		LocationID:        &locationID,
		CustomerContactID: &contactID,
		AssetID:           &assetID,
		JobType:           formPtr(r.FormValue("job_type")),
		Subtitle:          formPtr(r.FormValue("subtitle")),
		BillingType:       formPtr(r.FormValue("billing_type")),
		Notes:             formPtr(r.FormValue("notes")),
		TechNotes:         formPtr(r.FormValue("tech_notes")),
		CustomFields:      strPtr(parseCustomFieldValues(r)),
	}
	if v := r.FormValue("visits"); v != "" {
		visits := services.ParseVisits(v)
		params.Visits = &visits
	}
	if a := r.FormValue("assignments"); a != "" {
		assignments := services.ParseAssignments(a)
		params.Assignments = &assignments
	}
	if s := r.FormValue("subtasks"); s != "" {
		subtasks := services.ParseSubtasks(s)
		params.Subtasks = &subtasks
	}
	loc := middleware.CompanyLocation(r.Context())
	if st := r.FormValue("start_time"); st != "" {
		t := parseTime(st, loc)
		params.StartTime = &t
	}
	if et := r.FormValue("end_time"); et != "" {
		t := parseTime(et, loc)
		params.EndTime = &t
	}
	if dd := r.FormValue("due_date"); dd != "" {
		t := parseDate(dd, loc)
		params.DueDate = &t
	}
	updateJob := h.updateJob
	if updateJob == nil {
		updateJob = h.svc.Update
	}
	result, err := updateJob(r.Context(), u.CompanyID, id, params)
	if err != nil {
		if handleInvalidJobInput(w, r, "update", err) {
			return
		}
		internalServerError(w, r, "update job", err)
		return
	}
	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"entity_name": result.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=Job+updated", id), http.StatusSeeOther)
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := j.JobType
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs?flash=Job+archived", http.StatusSeeOther)
}

func (h *JobHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.svc.Restore(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"entity_name": j.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10)+"?flash=Job+restored", http.StatusSeeOther)
}

func (h *JobHandler) ToggleSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, id), policyUpdate) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	j, err := h.svc.GetByIDForCompany(r.Context(), u.CompanyID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subtasks := services.ParseSubtasks(j.Subtasks)
	if idx < 0 || idx >= len(subtasks) {
		http.NotFound(w, r)
		return
	}
	subtasks[idx].Completed = !subtasks[idx].Completed
	params := services.JobUpdateParams{
		Subtasks: &subtasks,
	}
	if _, err := h.svc.Update(r.Context(), u.CompanyID, id, params); err != nil {
		internalServerError(w, r, "toggle job subtask", err)
		return
	}
	if u != nil {
		action := "subtask_completed"
		if !subtasks[idx].Completed {
			action = "subtask_uncompleted"
		}
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, action, objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": subtasks[idx].Title,
		})
	}
	statuses := h.statusesForSelect(r.Context())
	d := jobToDetail(r.Context(), j, statuses)
	d.Subtasks = subtasks
	templates.JobSubtasks(d).Render(r.Context(), w)
}

func (h *JobHandler) AssetOptions(w http.ResponseWriter, r *http.Request) {
	customerID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
	assets, _ := h.assetSvc.ListForCustomer(r.Context(), customerID)
	templates.AssetOptions(assetOptions(assets), selected).Render(r.Context(), w)
}

func (h *JobHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "job")
	return statuses
}

func (h *JobHandler) validJobStatus(w http.ResponseWriter, r *http.Request, statusID int64, fd templates.JobFormPageData) bool {
	ok, err := h.statusSvc.BelongsToObjectType(r.Context(), statusID, "job")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if ok {
		return true
	}
	if fd.Job == nil {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return false
	}
	if fd.Errors == nil {
		fd.Errors = map[string]string{}
	}
	fd.Errors["status_id"] = "Select a valid job status"
	templates.JobForm(fd).Render(r.Context(), w)
	return false
}

func (h *JobHandler) jobFormFromRequest(r *http.Request, companyID, id int64) templates.JobFormPageData {
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("customer_contact_id"), 10, 64)
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	fd := h.newJobForm(r.Context(), companyID, customerID)
	fd.Job.ID = id
	fd.Job.CustomerID = customerID
	fd.Job.ProjectID = projectID
	fd.Job.LocationID = locationID
	fd.Job.ContactID = contactID
	fd.Job.AssetID = assetID
	fd.Job.JobType = r.FormValue("job_type")
	fd.Job.Subtitle = r.FormValue("subtitle")
	fd.Job.StatusID = statusID
	fd.Job.BillingType = r.FormValue("billing_type")
	fd.Job.StartTime = r.FormValue("start_time")
	fd.Job.EndTime = r.FormValue("end_time")
	fd.Job.DueDate = r.FormValue("due_date")
	fd.Job.Notes = r.FormValue("notes")
	fd.Job.TechNotes = r.FormValue("tech_notes")
	fd.ExistingVisitsJSON = r.FormValue("visits")
	fd.ExistingAssignmentsJSON = r.FormValue("assignments")
	fd.ExistingSubtasksJSON = r.FormValue("subtasks")
	return fd
}

func (h *JobHandler) newJobForm(ctx context.Context, companyID, customerID int64) templates.JobFormPageData {
	statuses := h.statusesForSelect(ctx)
	projectStatuses, _ := h.statusSvc.ByObjectType(ctx, "project")
	customers, _ := h.custSvc.ListAllForCompany(ctx, companyID)
	customerID = customerIDFromOptions(customers, customerID)
	var projects []*ent.Project
	var locations []*ent.Location
	var assets []*ent.Asset
	if customerID > 0 {
		projects, _ = h.projectSvc.ListByCustomer(ctx, customerID)
		locations, _ = h.locSvc.ListByCustomer(ctx, customerID)
		assets, _ = h.assetSvc.ListForCustomer(ctx, customerID)
	}
	assetTypes, _ := h.assetTypeSvc.List(ctx)
	assetStatuses, _ := h.assetStatSvc.List(ctx)
	users, _ := h.userSvc.ListActiveForCompany(ctx, companyID)
	defs, _ := h.defSvc.ListForObjectType(ctx, "job")
	return templates.JobFormPageData{
		Job: &templates.JobDetail{
			CustomerID:  customerID,
			BillingType: "flat_rate",
			StartTime:   time.Now().Format("2006-01-02") + "T08:00",
		},
		Customers:               customerOptions(customers),
		Projects:                projectOptions(projects),
		Locations:               locationOptions(locations),
		Assets:                  assetOptions(assets),
		AssetTypes:              assetTypesToOptions(assetTypes),
		AssetStatuses:           assetStatusesToOptions(assetStatuses),
		ProjectStatuses:         statusOptions(projectStatuses),
		Users:                   userOptions(users),
		Statuses:                statusOptions(statuses),
		BillingTypes:            services.JobBillingTypes,
		ExistingVisitsJSON:      "[]",
		ExistingAssignmentsJSON: "[]",
		ExistingSubtasksJSON:    "[]",
		CustomFields:            buildCustomFieldDisplay(defs, "[]"),
		Errors:                  map[string]string{},
	}
}

func (h *JobHandler) formDataFromJob(ctx context.Context, companyID int64, j *ent.Job, statuses []*ent.Status) (templates.JobFormPageData, error) {
	projectStatuses, _ := h.statusSvc.ByObjectType(ctx, "project")
	customers, _ := h.custSvc.ListAllForCompany(ctx, companyID)
	projects, _ := h.projectSvc.ListByCustomer(ctx, j.CustomerID)
	locations, _ := h.locSvc.ListByCustomer(ctx, j.CustomerID)
	assets, _ := h.assetSvc.ListForCustomer(ctx, j.CustomerID)
	assetTypes, _ := h.assetTypeSvc.List(ctx)
	assetStatuses, _ := h.assetStatSvc.List(ctx)
	users, _ := h.userSvc.ListActiveForCompany(ctx, companyID)
	defs, _ := h.defSvc.ListForObjectType(ctx, "job")
	d := jobToDetail(ctx, j, statuses)
	assignments, _ := h.svc.Assignments(ctx, j.ID)
	if len(assignments) == 0 {
		assignments = services.ParseAssignments(j.Assignments)
	}
	assignmentUsers, err := h.userSvc.ListByIDsForCompany(ctx, companyID, assignmentUserIDs(assignments))
	if err != nil {
		return templates.JobFormPageData{}, fmt.Errorf("list existing assignment users: %w", err)
	}
	return templates.JobFormPageData{
		Job:                     &d,
		Customers:               customerOptions(customers),
		Projects:                projectOptions(projects),
		Locations:               locationOptions(locations),
		Assets:                  assetOptions(assets),
		AssetTypes:              assetTypesToOptions(assetTypes),
		AssetStatuses:           assetStatusesToOptions(assetStatuses),
		ProjectStatuses:         statusOptions(projectStatuses),
		Users:                   userOptions(users),
		Statuses:                statusOptions(statuses),
		BillingTypes:            services.JobBillingTypes,
		ExistingVisitsJSON:      services.SerializeVisits(services.ParseVisits(j.Visits)),
		ExistingAssignmentsJSON: serializeJobFormAssignments(assignments, assignmentUsers),
		ExistingSubtasksJSON:    services.SerializeSubtasks(services.ParseSubtasks(j.Subtasks)),
		CustomFields:            buildCustomFieldDisplay(defs, j.CustomFields),
		Errors:                  map[string]string{},
	}, nil
}

type jobFormAssignment struct {
	UserID          int64  `json:"user_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	PreservedUserID int64  `json:"preserved_user_id,omitempty"`
}

func assignmentUserIDs(assignments []services.JobAssignment) []int64 {
	ids := make([]int64, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.UserID > 0 {
			ids = append(ids, assignment.UserID)
		}
	}
	return ids
}

func serializeJobFormAssignments(assignments []services.JobAssignment, tenantUsers []*ent.User) string {
	usersByID := make(map[int64]*ent.User, len(tenantUsers))
	for _, user := range tenantUsers {
		usersByID[user.ID] = user
	}

	items := make([]jobFormAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.UserID <= 0 {
			items = append(items, jobFormAssignment{Name: assignment.Name, Role: assignment.Role})
			continue
		}
		user := usersByID[assignment.UserID]
		if user == nil {
			continue
		}
		item := jobFormAssignment{UserID: user.ID, Name: user.Name, Role: assignment.Role}
		if !user.IsActive {
			item.PreservedUserID = user.ID
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return "[]"
	}
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

func customerIDFromOptions(customers []*ent.Customer, requestedID int64) int64 {
	for _, customer := range customers {
		if customer.ID == requestedID {
			return requestedID
		}
	}
	return 0
}

func userOptions(users []*ent.User) []templates.SelectOption {
	opts := make([]templates.SelectOption, 0, len(users))
	for _, u := range users {
		if !u.IsActive {
			continue
		}
		label := u.Name
		if u.Role != "" {
			label = fmt.Sprintf("%s (%s)", u.Name, u.Role)
		}
		opts = append(opts, templates.SelectOption{Value: u.ID, Label: label})
	}
	return opts
}

func projectOptions(projects []*ent.Project) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(projects))
	for i, p := range projects {
		opts[i] = templates.SelectOption{Value: p.ID, Label: p.Name}
	}
	return opts
}

func locationOptions(locations []*ent.Location) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(locations))
	for i, l := range locations {
		opts[i] = templates.SelectOption{Value: l.ID, Label: l.Title}
	}
	return opts
}

func assetOptions(assets []*ent.Asset) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(assets))
	for i, a := range assets {
		opts[i] = templates.SelectOption{Value: a.ID, Label: assetLabel(a)}
	}
	return opts
}

func assetLabel(a *ent.Asset) string {
	if a == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{a.Manufacturer, a.Model, a.SerialNumber} {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return a.Name
	}
	return strings.Join(parts, ", ")
}

func jobToDetail(ctx context.Context, j *ent.Job, statuses []*ent.Status) templates.JobDetail {
	d := templates.JobDetail{
		ID:          j.ID,
		CustomerID:  j.CustomerID,
		JobType:     j.JobType,
		Subtitle:    j.Subtitle,
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		StatusColor: statusColor(statuses, j.StatusID),
		Statuses:    statusOptions(statuses),
		BillingType: j.BillingType,
		Notes:       j.Notes,
		TechNotes:   j.TechNotes,
	}
	if j.ProjectID != nil {
		d.ProjectID = *j.ProjectID
	}
	if j.LocationID != nil {
		d.LocationID = *j.LocationID
	}
	if j.CustomerContactID != nil {
		d.ContactID = *j.CustomerContactID
	}
	if j.AssetID != nil {
		d.AssetID = *j.AssetID
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		d.StartTime = j.StartTime.Format("2006-01-02T15:04")
		d.StartTimeDisplay = displayDateTime(ctx, *j.StartTime)
	}
	if j.EndTime != nil && !j.EndTime.IsZero() {
		d.EndTime = j.EndTime.Format("2006-01-02T15:04")
		d.EndTimeDisplay = displayDateTime(ctx, *j.EndTime)
	}
	if j.DueDate != nil && !j.DueDate.IsZero() {
		d.DueDate = j.DueDate.Format("2006-01-02")
		d.DueDateDisplay = displayDate(ctx, *j.DueDate)
	}
	if j.DeletedAt != nil && !j.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *j.DeletedAt)
	}
	return d
}

func jobRow(ctx context.Context, j *ent.Job, statuses []*ent.Status, custMap map[int64]string) templates.JobRow {
	r := templates.JobRow{
		ID:          j.ID,
		DisplayName: j.JobType,
		JobType:     j.JobType,
		Customer:    custMap[j.CustomerID],
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		StatusColor: statusColor(statuses, j.StatusID),
		BillingType: j.BillingType,
	}
	if j.Subtitle != "" {
		r.DisplayName = j.JobType + " — " + j.Subtitle
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		r.StartTime = displayDateTime(ctx, *j.StartTime)
	}
	return r
}

func jobDisplayName(j *ent.Job) string {
	if j.Subtitle != "" {
		return j.JobType + " — " + j.Subtitle
	}
	return j.JobType
}

func parseTime(v string, loc *time.Location) time.Time {
	t, _ := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(v), loc)
	return t
}

func parseDate(v string, loc *time.Location) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", strings.TrimSpace(v), loc)
	return t
}

func statusID(j *ent.Job) int64 {
	if j.StatusID == nil {
		return 0
	}
	return *j.StatusID
}

func (h *JobHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeJob, id))
	if err != nil {
		writeTagError(w, err)
		return
	}
	if tag != nil {
		recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	h.loadTagWidget(w, r, id)
}

func (h *JobHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeJob, id)); err != nil {
		writeTagError(w, err)
		return
	}
	if tag != nil {
		recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeJob, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	h.loadTagWidget(w, r, id)
}

func (h *JobHandler) loadTagWidget(w http.ResponseWriter, r *http.Request, jobID int64) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeJob, jobID))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	d := templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/jobs/%d", jobID),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}
	templates.TagWidget(d).Render(r.Context(), w)
}

func tagsToRows(tags []*ent.Tag) []templates.TagRow {
	rows := make([]templates.TagRow, len(tags))
	for i, t := range tags {
		rows[i] = templates.TagRow{
			ID:    t.ID,
			Name:  t.Name,
			Color: t.Color,
		}
	}
	return rows
}
