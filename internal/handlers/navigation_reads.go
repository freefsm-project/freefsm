package handlers

import (
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func collectionRead(typ string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, ok := authorization.FromContext(r.Context())
			if !ok {
				authorizationError(w, r, authorization.ErrForbidden)
				return
			}
			p, err := req.Service.Principal(r.Context(), req.Actor)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			if typ != "" && !authorization.CollectionAllowed(p, typ) {
				authorizationError(w, r, authorization.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(authorization.WithNavigationReads(r.Context(), p)))
		})
	}
}

func scheduleDispatchVisible(r *http.Request) bool {
	if p, current := authorization.NavigationPrincipal(r.Context()); current {
		return p.Allows("job.view", authorization.Assigned)
	}
	return false
}
