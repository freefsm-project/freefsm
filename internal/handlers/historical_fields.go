package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
)

func (h *CustomFieldHandler) HistoricalReview(w http.ResponseWriter, r *http.Request) {
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	cycle := chi.URLParam(r, "cycle")
	if r.Method == http.MethodGet {
		snapshot, err := bound.Service.HistoricalFieldSnapshot(r.Context(), bound.Actor, cycle)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		templates.HistoricalFieldReview(cycle, snapshot).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	var evidence customfields.CycleEvidence
	decoder := json.NewDecoder(strings.NewReader(r.PostForm.Get("evidence")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil || r.PostForm.Get("reviewed") != "true" {
		http.Error(w, "Explicit reviewed historical field evidence is required", 422)
		return
	}
	if err := bound.Service.ReviewHistoricalFields(r.Context(), bound.Actor, cycle, r.PostForm.Get("reason"), evidence); err != nil {
		writeConversionError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/custom-fields", 303)
}
