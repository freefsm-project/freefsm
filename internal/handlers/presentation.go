package handlers

import (
	"log/slog"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func presentationContext(s *authorization.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := middleware.UserFromContext(r.Context())
			legacy := ""
			if cookie, err := r.Cookie("theme"); err == nil {
				legacy = cookie.Value
			}
			theme, err := s.Presentation(r.Context(), authorizationActor(r), legacy)
			if err != nil {
				slog.Error("load presentation", "path", r.URL.Path, "error", err)
				http.Error(w, "Presentation settings are unavailable. Retry this page.", http.StatusServiceUnavailable)
				return
			}
			u.Theme = theme
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

func presentationPreferences(s *authorization.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := middleware.UserFromContext(r.Context())
		data := templates.PreferencesData{Theme: u.Theme, FontSize: normalizedFontSize(u.FontSize)}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid form", 400)
				return
			}
			data.Theme, data.FontSize = r.FormValue("theme"), r.FormValue("font_size")
			if data.Theme == "" {
				data.Theme = u.Theme
			} // preserve old font-only clients
			if data.FontSize == "" {
				data.FontSize = u.FontSize
			}
			if (data.Theme != "system" && data.Theme != "light" && data.Theme != "dark") || (data.FontSize != "small" && data.FontSize != "medium" && data.FontSize != "large") {
				saveaction.RolledBack(r.Context()) // rejected before any domain write
				data.Error = "Choose a listed theme and font size. Your preferences were not saved."
				w.WriteHeader(http.StatusUnprocessableEntity)
			} else {
				if err := s.SavePresentation(r.Context(), authorizationActor(r), data.Theme, data.FontSize); err != nil {
					authorizationError(w, r, err)
					return
				}
				http.Redirect(w, r, "/preferences?flash=Preferences+saved", http.StatusSeeOther)
				return
			}
		}
		templates.PreferencesPage(data).Render(r.Context(), w)
	}
}
