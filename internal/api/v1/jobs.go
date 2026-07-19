package v1

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
)

type statusDTO struct {
	ID       int64                  `json:"id"`
	Name     string                 `json:"name"`
	Color    string                 `json:"color"`
	Category statusflow.CategoryKey `json:"category"`
}

type jobDTO struct {
	ID                 int64                    `json:"id"`
	JobType            string                   `json:"job_type"`
	Subtitle           string                   `json:"subtitle"`
	Status             *statusDTO               `json:"status"`
	StartTime          *time.Time               `json:"start_time"`
	EndTime            *time.Time               `json:"end_time"`
	DueDate            *time.Time               `json:"due_date"`
	ArrivalWindowStart *time.Time               `json:"arrival_window_start"`
	ArrivalWindowEnd   *time.Time               `json:"arrival_window_end"`
	Notes              string                   `json:"notes"`
	TechNotes          string                   `json:"tech_notes"`
	Visits             []services.JobVisit      `json:"visits"`
	Assignments        []services.JobAssignment `json:"assignments"`
	Subtasks           []services.JobSubtask    `json:"subtasks"`
	CustomerID         *int64                   `json:"customer_id,omitempty"`
	ProjectID          *int64                   `json:"project_id,omitempty"`
	LocationID         *int64                   `json:"location_id,omitempty"`
	CustomerContactID  *int64                   `json:"customer_contact_id,omitempty"`
	AssetID            *int64                   `json:"asset_id,omitempty"`
	BillingType        *string                  `json:"billing_type,omitempty"`
	LineItems          *[]services.LineItem     `json:"line_items,omitempty"`
}

func (h *handler) listJobs(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r.Context())
	var jobs []*ent.Job
	var err error
	if isOffice(actor.Role) {
		jobs, err = h.deps.Jobs.ListAllForCompany(r.Context(), *actor.CompanyID)
	} else if isTech(actor.Role) {
		jobs, err = h.deps.Jobs.ListAssignedAllForCompany(r.Context(), *actor.CompanyID, actor.ID)
	} else {
		writeError(w, http.StatusForbidden, "forbidden", "role cannot access jobs")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list jobs")
		return
	}
	statuses, err := h.jobStatuses(r, *actor.CompanyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load job statuses")
		return
	}
	result := make([]jobDTO, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, makeJobDTO(job, statuses, actor.Role))
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	job, actor, ok := h.authorizedJob(w, r)
	if !ok {
		return
	}
	statuses, err := h.jobStatuses(r, *actor.CompanyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load job statuses")
		return
	}
	writeJSON(w, http.StatusOK, makeJobDTO(job, statuses, actor.Role))
}

type statusRequest struct {
	StatusID int64 `json:"status_id"`
}

func (h *handler) updateJobStatus(w http.ResponseWriter, r *http.Request) {
	job, actor, ok := h.authorizedJob(w, r)
	if !ok {
		return
	}
	if isTech(actor.Role) && !h.requireOpenJob(w, r, job, *actor.CompanyID) {
		return
	}
	var request statusRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.StatusID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "status_id must be positive")
		return
	}
	err := h.deps.StatusFlow.TransitionJob(r.Context(), statusflow.Actor{ID: actor.ID, CompanyID: *actor.CompanyID}, job.ID, request.StatusID)
	if err != nil {
		writeStatusFlowError(w, err)
		return
	}
	updated, err := h.deps.Jobs.GetByIDForCompany(r.Context(), *actor.CompanyID, job.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "status changed but job could not be reloaded")
		return
	}
	statuses, err := h.jobStatuses(r, *actor.CompanyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "status changed but statuses could not be loaded")
		return
	}
	writeJSON(w, http.StatusOK, makeJobDTO(updated, statuses, actor.Role))
}

type subtaskRequest struct {
	Completed *bool `json:"completed"`
}

func (h *handler) updateSubtask(w http.ResponseWriter, r *http.Request) {
	job, actor, ok := h.authorizedJob(w, r)
	if !ok {
		return
	}
	if !h.requireOpenJob(w, r, job, *actor.CompanyID) {
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest, "invalid_index", "subtask index must be a non-negative integer")
		return
	}
	var request subtaskRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Completed == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "completed is required")
		return
	}
	updated, err := h.deps.Mutations.SetSubtaskCompletion(r.Context(), mutationActor(actor), job.ID, index, *request.Completed)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrMutationForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "job is not assigned to this user")
			return
		case errors.Is(err, services.ErrMutationJobClosed):
			writeError(w, http.StatusConflict, "job_closed", "closed jobs cannot be modified")
			return
		case errors.Is(err, services.ErrMutationJobNotFound):
			writeError(w, http.StatusNotFound, "job_not_found", "job not found")
			return
		case errors.Is(err, services.ErrSubtaskNotFound):
			writeError(w, http.StatusNotFound, "subtask_not_found", "subtask not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not update subtask")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *handler) authorizedJob(w http.ResponseWriter, r *http.Request) (*ent.Job, *ent.User, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "job id must be a positive integer")
		return nil, nil, false
	}
	actor := actorFromContext(r.Context())
	job, err := h.deps.Jobs.GetByIDForCompany(r.Context(), *actor.CompanyID, id)
	if err != nil || job == nil || job.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "job_not_found", "job not found")
		return nil, nil, false
	}
	if isOffice(actor.Role) {
		return job, actor, true
	}
	if !isTech(actor.Role) || !h.deps.Policy.IsUserAssignedToJob(r.Context(), id, actor.ID) {
		writeError(w, http.StatusForbidden, "forbidden", "job is not assigned to this user")
		return nil, nil, false
	}
	return job, actor, true
}

func (h *handler) jobStatuses(r *http.Request, companyID int64) (map[int64]statusDTO, error) {
	items, err := h.deps.Statuses.ByObjectTypeForCompany(r.Context(), companyID, "job")
	if err != nil {
		return nil, err
	}
	result := make(map[int64]statusDTO, len(items))
	for _, status := range items {
		result[status.ID] = statusDTO{ID: status.ID, Name: status.Name, Color: status.Color, Category: statusflow.CategoryKey(status.CategoryKey)}
	}
	return result, nil
}

func (h *handler) requireOpenJob(w http.ResponseWriter, r *http.Request, job *ent.Job, companyID int64) bool {
	if job.StatusID == nil {
		return true
	}
	statuses, err := h.jobStatuses(r, companyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load job status")
		return false
	}
	if status, ok := statuses[*job.StatusID]; ok && statusflow.IsClosed(status.Category) {
		writeError(w, http.StatusConflict, "job_closed", "closed jobs cannot be modified")
		return false
	}
	return true
}

func makeJobDTO(job *ent.Job, statuses map[int64]statusDTO, role string) jobDTO {
	dto := jobDTO{ID: job.ID, JobType: job.JobType, Subtitle: job.Subtitle, StartTime: job.StartTime, EndTime: job.EndTime, DueDate: job.DueDate, ArrivalWindowStart: job.ArrivalWindowStart, ArrivalWindowEnd: job.ArrivalWindowEnd, Notes: job.Notes, TechNotes: job.TechNotes, Visits: services.ParseVisits(job.Visits), Assignments: services.ParseAssignments(job.Assignments), Subtasks: services.ParseSubtasks(job.Subtasks)}
	if dto.Visits == nil {
		dto.Visits = []services.JobVisit{}
	}
	if dto.Assignments == nil {
		dto.Assignments = []services.JobAssignment{}
	}
	if dto.Subtasks == nil {
		dto.Subtasks = []services.JobSubtask{}
	}
	if job.StatusID != nil {
		if status, ok := statuses[*job.StatusID]; ok {
			dto.Status = &status
		}
	}
	if isOffice(role) {
		dto.CustomerID = &job.CustomerID
		dto.ProjectID = job.ProjectID
		dto.LocationID = job.LocationID
		dto.CustomerContactID = job.CustomerContactID
		dto.AssetID = job.AssetID
		dto.BillingType = &job.BillingType
		lineItems, _ := services.DecodeLineItems(job.LineItems)
		if lineItems == nil {
			lineItems = []services.LineItem{}
		}
		dto.LineItems = &lineItems
	}
	return dto
}

func writeStatusFlowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, statusflow.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "status change is not allowed")
	case errors.Is(err, statusflow.ErrNotFound):
		writeError(w, http.StatusNotFound, "job_not_found", "job not found")
	case errors.Is(err, statusflow.ErrWrongType), errors.Is(err, statusflow.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_status", "status is not valid for jobs")
	case errors.Is(err, statusflow.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_transition", "status transition is not allowed")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not update job status")
	}
}

func isOffice(role string) bool { return role == "admin" || role == "dispatcher" }
func isTech(role string) bool   { return role == "tech" || role == "technician" }
