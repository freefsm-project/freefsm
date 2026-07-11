package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/objectref"
	"github.com/go-chi/chi/v5"
)

func requireActiveObject(objects objectref.Directory, objectType objectref.Type) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			if err != nil || id <= 0 {
				http.NotFound(w, r)
				return
			}
			ref := objectref.New(objectType, id)
			if !ref.Valid() || !objects.Supports(objectType, objectref.CapArchive) {
				http.NotFound(w, r)
				return
			}
			active, err := objects.Exists(r.Context(), ref, objectref.ExistsActive)
			if err != nil {
				if errors.Is(err, objectref.ErrActiveUnsupported) || errors.Is(err, objectref.ErrUnknownType) || errors.Is(err, objectref.ErrInvalidID) {
					http.NotFound(w, r)
					return
				}
				http.Error(w, "verify active object", http.StatusInternalServerError)
				return
			}
			if !active {
				http.Error(w, "archived records are read-only", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
