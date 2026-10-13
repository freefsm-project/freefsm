package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func sessionManagement(s *authorization.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := authorizationActor(r)
		subject := a.UserID
		if raw := chi.URLParam(r, "userID"); raw != "" {
			var err error
			subject, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || subject <= 0 {
				http.NotFound(w, r)
				return
			}
		}
		base := "/sessions"
		if subject != a.UserID {
			base = "/users/" + strconv.FormatInt(subject, 10) + "/sessions"
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost {
			var id int64
			if raw := chi.URLParam(r, "sessionID"); raw != "" {
				var err error
				id, err = strconv.ParseInt(raw, 10, 64)
				if err != nil || id <= 0 {
					http.NotFound(w, r)
					return
				}
			}
			if err := s.RevokeSessions(r.Context(), a, subject, id); err != nil {
				authorizationError(w, r, err)
				return
			}
			if subject == a.UserID {
				if _, err := s.Principal(r.Context(), a); errors.Is(err, authorization.ErrForbidden) {
					base = "/login"
					http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1})
				}
			}
			http.Redirect(w, r, base, http.StatusSeeOther)
			return
		}
		list, err := s.Sessions(r.Context(), a, subject)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		render(w, r, templates.SessionsPage(list, base))
	}
}
