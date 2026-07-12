package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type TagHandler struct {
	svc         *services.TagService
	linkSvc     *services.TagLinkService
	activitySvc *services.ActivityService
	depSvc      *services.DependencyService
}

func NewTagHandler(svc *services.TagService, linkSvc *services.TagLinkService, activitySvc *services.ActivityService, depSvc *services.DependencyService) *TagHandler {
	return &TagHandler{svc: svc, linkSvc: linkSvc, activitySvc: activitySvc, depSvc: depSvc}
}

func (h *TagHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	tags, err := h.svc.ListAll(r.Context(), u.CompanyID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	rows := make([]templates.TagRow, len(tags))
	for i, t := range tags {
		rows[i] = templates.TagRow{
			ID:    t.ID,
			Name:  t.Name,
			Color: t.Color,
		}
	}

	data := templates.TagListPageData{
		Tags: rows,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.TagsTable(data).Render(r.Context(), w)
		return
	}
	templates.TagsIndex(data).Render(r.Context(), w)
}

func (h *TagHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		templates.TagForm(templates.TagFormData{IsNew: true}).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	name := r.FormValue("name")
	color := r.FormValue("color")
	if name == "" {
		data := templates.TagFormData{
			IsNew:  true,
			Errors: map[string]string{"name": "Name is required"},
		}
		templates.TagForm(data).Render(r.Context(), w)
		return
	}
	result, err := h.svc.Create(r.Context(), u.CompanyID, name, color)
	if err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_created", objectref.New(objectref.TypeTag, result.ID), map[string]interface{}{
		"entity_name": result.Name,
		"actor_name":  u.Name,
	})
	http.Redirect(w, r, "/tags?flash=Tag+created", http.StatusSeeOther)
}

func (h *TagHandler) Update(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		t, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := templates.TagFormData{
			Tag: templates.TagRow{
				ID:    t.ID,
				Name:  t.Name,
				Color: t.Color,
			},
			IsNew: false,
		}
		templates.TagForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	name := r.FormValue("name")
	color := r.FormValue("color")
	if name == "" {
		t, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
		if err != nil {
			writeTagError(w, err)
			return
		}
		data := templates.TagFormData{
			Tag: templates.TagRow{
				ID:    t.ID,
				Name:  t.Name,
				Color: t.Color,
			},
			IsNew:  false,
			Errors: map[string]string{"name": "Name is required"},
		}
		templates.TagForm(data).Render(r.Context(), w)
		return
	}
	_, err = h.svc.Update(r.Context(), u.CompanyID, id, name, color)
	if err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_updated", objectref.New(objectref.TypeTag, id), map[string]interface{}{
		"entity_name": name,
		"actor_name":  u.Name,
	})
	http.Redirect(w, r, "/tags?flash=Tag+updated", http.StatusSeeOther)
}

func (h *TagHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	canDelete, reason, err := h.depSvc.CanDeleteTag(r.Context(), u.CompanyID, id)
	if err != nil {
		internalServerError(w, r, "check tag dependencies", err)
		return
	}
	if !canDelete {
		http.Error(w, reason, http.StatusConflict)
		return
	}
	tag, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
	if err != nil {
		http.Error(w, "tag not found", 404)
		return
	}
	entityName := tag.Name
	if err := h.svc.Delete(r.Context(), u.CompanyID, id); err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_deleted", objectref.New(objectref.TypeTag, id), map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
	})
	http.Redirect(w, r, "/tags?flash=Tag+deleted", http.StatusSeeOther)
}

func requireTagCompany(w http.ResponseWriter, r *http.Request) (*middleware.UserInfo, bool) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}

func writeTagError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrTagInvalid):
		http.Error(w, "invalid tag request", http.StatusBadRequest)
	case errors.Is(err, services.ErrTagNotFound):
		http.Error(w, "tag or target not found", http.StatusNotFound)
	case errors.Is(err, services.ErrTagConflict):
		http.Error(w, "tag conflict", http.StatusConflict)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func tagRouteIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid object id", http.StatusBadRequest)
		return 0, 0, false
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	if err != nil || tagID <= 0 {
		http.Error(w, "invalid tag id", http.StatusBadRequest)
		return 0, 0, false
	}
	return id, tagID, true
}

// The tag mutation has already committed; activity is intentionally best-effort.
func recordTagActivity(r *http.Request, svc *services.ActivityService, u *middleware.UserInfo, action string, target objectref.Ref, metadata map[string]interface{}) {
	if err := svc.Record(r.Context(), u.CompanyID, u.ID, action, target, metadata); err != nil {
		slog.Error("record tag activity", "error", err, "operation", action, "object_type", target.Type, "object_id", target.ID, "company_id", u.CompanyID, "actor_id", u.ID)
	}
}
