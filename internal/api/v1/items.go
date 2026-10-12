package v1

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
)

func (h *handler) itemReferences(w http.ResponseWriter, r *http.Request) {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 404, "not_found", "not found")
		return
	}
	for key := range r.URL.Query() {
		if key != "search" {
			writeError(w, 400, "invalid_filter", "unknown Item filter")
			return
		}
	}
	var id int64
	if raw := chi.URLParam(r, "id"); raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "not found")
			return
		}
	}
	items, err := request.Service.ItemReferences(r.Context(), request.Actor, r.URL.Query().Get("search"), id, strings.HasSuffix(r.URL.Path, "/selection"))
	if err != nil {
		if errors.Is(err, authorization.ErrForbidden) {
			writeError(w, 403, "forbidden", "Item action denied")
		} else if errors.Is(err, authorization.ErrNotFound) {
			writeError(w, 404, "not_found", "not found")
		} else {
			writeError(w, 503, "unavailable", "Item lookup unavailable")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if id > 0 {
		writeJSON(w, 200, items[0])
		return
	}
	writeJSON(w, 200, struct {
		Items []authorization.ItemReference `json:"items"`
	}{items})
}
