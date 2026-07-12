package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type DocumentStatusHandler struct{ svc *services.StatusService }

func NewDocumentStatusHandler(svc *services.StatusService) *DocumentStatusHandler {
	return &DocumentStatusHandler{svc: svc}
}

func documentStatusType(r *http.Request) (string, bool) {
	typ := chi.URLParam(r, "type")
	return typ, typ == "estimate" || typ == "invoice"
}

func (h *DocumentStatusHandler) List(w http.ResponseWriter, r *http.Request) {
	typ, ok := documentStatusType(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	statuses, err := h.svc.ByObjectType(r.Context(), typ)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := make([]templates.DocumentStatusRow, len(statuses))
	for i, s := range statuses {
		rows[i] = templates.DocumentStatusRow{ID: s.ID, Name: s.Name, Color: s.Color, SortOrder: s.SortOrder, Convertible: s.EstimateConvertible, Draft: s.DocumentRole == "draft"}
	}
	templates.DocumentStatusSettingsPage(templates.DocumentStatusPageData{ObjectType: typ, Statuses: rows}).Render(r.Context(), w)
}

func (h *DocumentStatusHandler) Create(w http.ResponseWriter, r *http.Request) {
	typ, ok := documentStatusType(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	order, _ := strconv.Atoi(r.FormValue("sort_order"))
	if _, err := h.svc.CreateForObjectType(r.Context(), typ, r.FormValue("name"), r.FormValue("color"), order); err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	h.redirect(w, r, typ, "Status created")
}

func (h *DocumentStatusHandler) Update(w http.ResponseWriter, r *http.Request) {
	typ, ok := documentStatusType(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	belongs, err := h.svc.BelongsToObjectType(r.Context(), id, typ)
	if err != nil || !belongs {
		http.Error(w, "status does not belong to this workflow", 400)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	role := "standard"
	if r.FormValue("document_role") == "draft" {
		role = "draft"
	}
	if _, err = h.svc.UpdateDocumentCapabilities(r.Context(), id, typ == "estimate" && r.FormValue("estimate_convertible") == "true", role); err != nil {
		if errors.Is(err, services.ErrDraftStatusRequired) || errors.Is(err, services.ErrInvalidStatusCapability) {
			h.redirect(w, r, typ, err.Error())
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	order, _ := strconv.Atoi(r.FormValue("sort_order"))
	if _, err = h.svc.Update(r.Context(), id, r.FormValue("name"), r.FormValue("color"), order); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.redirect(w, r, typ, "Status updated")
}

func (h *DocumentStatusHandler) redirect(w http.ResponseWriter, r *http.Request, typ, message string) {
	http.Redirect(w, r, "/settings/document-statuses/"+typ+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}
