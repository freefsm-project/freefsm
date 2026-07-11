package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type JobStatusHandler struct {
	svc         *services.StatusService
	activitySvc *services.ActivityService
}

func NewJobStatusHandler(svc *services.StatusService, activitySvc *services.ActivityService) *JobStatusHandler {
	return &JobStatusHandler{svc: svc, activitySvc: activitySvc}
}

func (h *JobStatusHandler) List(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.svc.ByObjectType(r.Context(), "job")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]templates.JobStatusRow, len(statuses))
	for i, s := range statuses {
		usage, err := h.svc.CountObjectUsage(r.Context(), "job", s.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows[i] = jobStatusRow(s, usage)
	}
	templates.JobStatusSettingsPage(templates.JobStatusListPageData{Statuses: rows}).Render(r.Context(), w)
}

func (h *JobStatusHandler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}
	color := r.FormValue("color")
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	result, err := h.svc.CreateForObjectType(r.Context(), "job", name, color, sortOrder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.recordActivity(r, "status_created", result)
	http.Redirect(w, r, "/settings/job-statuses?flash=Status+created", http.StatusSeeOther)
}

func (h *JobStatusHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ok, err := h.svc.BelongsToObjectType(r.Context(), id, "job")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "status does not belong to jobs", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}
	color := r.FormValue("color")
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	result, err := h.svc.Update(r.Context(), id, name, color, sortOrder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.recordActivity(r, "status_updated", result)
	http.Redirect(w, r, "/settings/job-statuses?flash=Status+updated", http.StatusSeeOther)
}

func (h *JobStatusHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	existing, err := h.getJobStatus(r, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	var replacement *int64
	if raw := r.FormValue("replacement_status_id"); raw != "" && raw != "0" {
		replacementID, _ := strconv.ParseInt(raw, 10, 64)
		replacement = &replacementID
	}
	if err := h.svc.Delete(r.Context(), "job", id, replacement); err != nil {
		if errors.Is(err, services.ErrReplacementStatusNeeded) || errors.Is(err, services.ErrInvalidReplacementStatus) {
			http.Redirect(w, r, "/settings/job-statuses?flash="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.recordActivity(r, "status_deleted", existing)
	http.Redirect(w, r, "/settings/job-statuses?flash=Status+deleted", http.StatusSeeOther)
}

func (h *JobStatusHandler) getJobStatus(r *http.Request, id int64) (*ent.Status, error) {
	statuses, err := h.svc.ByObjectType(r.Context(), "job")
	if err != nil {
		return nil, err
	}
	for _, s := range statuses {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, errors.New("status does not belong to jobs")
}

func (h *JobStatusHandler) recordActivity(r *http.Request, action string, s *ent.Status) {
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		return
	}
	h.activitySvc.Record(r.Context(), u.ID, action, objectref.New(objectref.TypeJobStatus, s.ID), map[string]interface{}{
		"entity_name": s.Name,
		"actor_name":  u.Name,
	})
}

func jobStatusRow(s *ent.Status, usage int) templates.JobStatusRow {
	return templates.JobStatusRow{ID: s.ID, Name: s.Name, Color: s.Color, SortOrder: s.SortOrder, UsageCount: usage}
}
