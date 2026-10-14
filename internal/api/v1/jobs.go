package v1

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
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
	Order    int                    `json:"order"`
}

type jobDTO struct {
	ID                     int64                 `json:"id"`
	JobType                string                `json:"job_type"`
	Subtitle               string                `json:"subtitle"`
	Status                 *statusDTO            `json:"status"`
	StartTime              *time.Time            `json:"start_time"`
	EndTime                *time.Time            `json:"end_time"`
	DueDate                *string               `json:"due_date"`
	ArrivalWindowStart     *time.Time            `json:"arrival_window_start"`
	ArrivalWindowEnd       *time.Time            `json:"arrival_window_end"`
	Notes                  string                `json:"notes"`
	TechNotes              string                `json:"tech_notes"`
	Visits                 []services.JobVisit   `json:"visits"`
	Assignments            []jobAssigneeDTO      `json:"assignments"`
	Subtasks               []services.JobSubtask `json:"subtasks"`
	SubtasksReviewRequired bool                  `json:"subtasks_review_required"`
	CustomerID             *int64                `json:"customer_id,omitempty"`
	ProjectID              *int64                `json:"project_id,omitempty"`
	LocationID             *int64                `json:"location_id,omitempty"`
	CustomerContactID      *int64                `json:"customer_contact_id,omitempty"`
	AssetID                *int64                `json:"asset_id,omitempty"`
	BillingType            *string               `json:"billing_type,omitempty"`
	LineItems              *[]services.LineItem  `json:"line_items,omitempty"`
}

type jobAssigneeDTO struct {
	UserID      int64  `json:"user_id"`
	Name        string `json:"name"`
	Unavailable bool   `json:"unavailable"`
}

func (h *handler) listJobs(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r.Context())
	var jobs []*ent.Job
	var err error
	if req, ok := authorization.FromContext(r.Context()); ok {
		p, e := req.Service.Principal(r.Context(), req.Actor)
		if e != nil {
			jobExecutionAPIError(w, e)
			return
		}
		if p.Allows("job.view", authorization.All) {
			jobs, err = h.deps.Jobs.ListAllForCompany(r.Context(), *actor.CompanyID)
		} else if p.Allows("job.view", authorization.Assigned) {
			jobs, err = h.deps.Jobs.ListAssignedAllForCompany(r.Context(), *actor.CompanyID, actor.ID)
		} else {
			writeError(w, 403, "forbidden", "Job View required")
			return
		}
	} else if isOffice(actor.Role) {
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
		if req, ok := authorization.FromContext(r.Context()); ok {
			if err := req.Service.ParentAccess(r.Context(), req.Actor, "job", job.ID, ""); err != nil {
				if errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrForbidden) {
					continue
				}
				jobExecutionAPIError(w, err)
				return
			}
		}
		dto, err := h.visibleJobDTO(r, job, statuses, actor.Role)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not load Job assignees")
			return
		}
		result = append(result, dto)
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
	dto, err := h.visibleJobDTO(r, job, statuses, actor.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load Job assignees")
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

type statusRequest struct {
	statusflow.Intent
	OperationID string     `json:"operation_id"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}

func (h *handler) jobStatusIntents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "job not found")
		return
	}
	actor := actorFromContext(r.Context())
	choices, err := h.deps.StatusFlow.DiscoverIntents(r.Context(), statusflow.Actor{ID: actor.ID, CompanyID: *actor.CompanyID}, statusflow.Job, id)
	if err != nil {
		writeStatusFlowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, choices)
}

func (h *handler) updateJobStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "job not found")
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
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	result, err := req.Service.SubmitJobStatus(r.Context(), req.Actor, id, authorization.JobExecutionCommand{OperationID: request.OperationID, StatusID: request.StatusID, StatusRevision: request.Revision, MeaningRevision: request.MeaningRevision, OccurredAt: request.OccurredAt})
	if err != nil {
		jobExecutionAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type subtaskRequest struct {
	Completed          *bool      `json:"completed"`
	OperationID        string     `json:"operation_id"`
	CompletionRevision string     `json:"completion_revision"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty"`
}

func (h *handler) updateSubtask(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "not found")
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
	action := "reopen"
	if *request.Completed {
		action = "complete"
	}
	result, err := req.Service.SubmitSubtask(r.Context(), req.Actor, id, authorization.SubtaskCommand{OperationID: request.OperationID, Action: action, SubtaskID: chi.URLParam(r, "subtask"), CompletionRevision: request.CompletionRevision, OccurredAt: request.OccurredAt})
	if err != nil {
		subtaskAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *handler) authorizedJob(w http.ResponseWriter, r *http.Request) (*ent.Job, *ent.User, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "job id must be a positive integer")
		return nil, nil, false
	}
	actor := actorFromContext(r.Context())
	if req, ok := authorization.FromContext(r.Context()); ok {
		if err := req.Service.ParentAccess(r.Context(), req.Actor, "job", id, ""); err != nil {
			jobExecutionAPIError(w, err)
			return nil, nil, false
		}
		job, err := h.deps.Jobs.GetByIDForCompany(r.Context(), *actor.CompanyID, id)
		if err != nil {
			writeError(w, 404, "not_found", "Job not found")
			return nil, nil, false
		}
		return job, actor, true
	}
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
		result[status.ID] = statusDTO{ID: status.ID, Name: status.Name, Color: status.Color, Category: statusflow.CategoryKey(status.CategoryKey), Order: status.CategoryOrder}
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

func (h *handler) visibleJobDTO(r *http.Request, job *ent.Job, statuses map[int64]statusDTO, role string) (jobDTO, error) {
	dto := makeJobDTO(job, statuses, role)
	assignments, err := h.deps.Jobs.Assignments(r.Context(), job.ID)
	if err != nil {
		return jobDTO{}, err
	}
	dto.Assignments = []jobAssigneeDTO{}
	for _, a := range assignments {
		dto.Assignments = append(dto.Assignments, jobAssigneeDTO{UserID: a.UserID, Name: a.Name, Unavailable: a.Unavailable})
	}
	if request, ok := authorization.FromContext(r.Context()); ok {
		if request.Service.ParentFacet(r.Context(), request.Actor, "job", job.ID, "pricing.view") != nil {
			dto.BillingType = nil
			dto.LineItems = nil
		}
	}
	if request, ok := authorization.FromContext(r.Context()); ok && dto.CustomerID != nil {
		if request.Service.CustomerAccess(r.Context(), request.Actor, job.CustomerID, "customer.view") != nil {
			if _, err := request.Service.JobCustomer(r.Context(), request.Actor, job.ID); err != nil {
				dto.CustomerID = nil
			}
		}
	}
	return dto, nil
}

func makeJobDTO(job *ent.Job, statuses map[int64]statusDTO, role string) jobDTO {
	dto := jobDTO{ID: job.ID, JobType: job.JobType, Subtitle: job.Subtitle, StartTime: job.StartTime, EndTime: job.EndTime, ArrivalWindowStart: job.ArrivalWindowStart, ArrivalWindowEnd: job.ArrivalWindowEnd, Notes: job.Notes, TechNotes: job.TechNotes, Visits: []services.JobVisit{}, Assignments: []jobAssigneeDTO{}, Subtasks: services.ParseSubtasks(job.Subtasks)}
	if job.DueDate != nil {
		date := job.DueDate.Format("2006-01-02")
		dto.DueDate = &date
	}
	for _, a := range services.ParseAssignments(job.Assignments) {
		dto.Assignments = append(dto.Assignments, jobAssigneeDTO{UserID: a.UserID, Name: a.Name})
	}
	if dto.Visits == nil {
		dto.Visits = []services.JobVisit{}
	}
	if dto.Subtasks == nil {
		dto.Subtasks = []services.JobSubtask{}
	}
	// Legacy positional rows are evidence awaiting review, not field targets.
	// The dedicated checklist endpoint exposes the explicit review disposition.
	for _, task := range dto.Subtasks {
		if task.ID == "" || task.CompletionRevision == "" {
			dto.Subtasks = []services.JobSubtask{}
			dto.SubtasksReviewRequired = true
			break
		}
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
	if errors.Is(err, statusflow.ErrStaleIntent) {
		writeError(w, 409, "status_reselection_required", "Status identity or meaning changed; explicitly reselect the target.")
		return
	}
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
