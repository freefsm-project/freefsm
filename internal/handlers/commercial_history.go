package handlers

import (
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func commercialHistoryHandler(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		changes, err := req.Service.CommercialHistory(r.Context(), req.Actor, kind, id)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		render(w, r, templates.CommercialHistory(changes))
	}
}
