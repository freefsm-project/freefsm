package handlers

import (
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type TagHandler struct {
	svc         *services.TagService
	linkSvc     *services.TagLinkService
	activitySvc *services.ActivityService
}

func NewTagHandler(svc *services.TagService, linkSvc *services.TagLinkService, activitySvc *services.ActivityService) *TagHandler {
	return &TagHandler{svc: svc, linkSvc: linkSvc, activitySvc: activitySvc}
}

func (h *TagHandler) List(w http.ResponseWriter, r *http.Request) {
	tags, err := h.svc.ListAll(r.Context())
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
	result, err := h.svc.Create(r.Context(), name, color)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_created", "tag", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/tags?flash=Tag+created", http.StatusSeeOther)
}

func (h *TagHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		t, err := h.svc.GetByID(r.Context(), id)
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
		t, _ := h.svc.GetByID(r.Context(), id)
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
	_, err = h.svc.Update(r.Context(), id, name, color)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_updated", "tag", id, map[string]interface{}{
			"entity_name": name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/tags?flash=Tag+updated", http.StatusSeeOther)
}

func (h *TagHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	tag, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "tag not found", 404)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_deleted", "tag", id, map[string]interface{}{
			"entity_name": tag.Name,
			"actor_name":  u.Name,
		})
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/tags?flash=Tag+deleted", http.StatusSeeOther)
}
