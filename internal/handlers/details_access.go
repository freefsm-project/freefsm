package handlers

import (
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func detailsAccess(typ string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request, ok := authorization.FromContext(r.Context())
			if !ok || request.Service == nil {
				http.Error(w, "Unauthorized", 401)
				return
			}
			id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			if err := request.Service.DetailsAccess(r.Context(), request.Actor, typ, id); err != nil {
				authorizationError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func authorizeFieldDocumentDestination(w http.ResponseWriter, r *http.Request, customerID, jobID int64) bool {
	request, _ := authorization.FromContext(r.Context())
	if _, err := request.Service.DocumentCustomer(r.Context(), request.Actor, customerID, jobID, "0"); err != nil {
		authorizationError(w, r, err)
		return false
	}
	if jobID > 0 {
		if err := request.Service.ParentAccess(r.Context(), request.Actor, "job", jobID, ""); err != nil {
			authorizationError(w, r, err)
			return false
		}
	}
	return true
}
