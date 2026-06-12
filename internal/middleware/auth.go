package middleware

import (
	"context"
	"net/http"

	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/justinas/nosurf"
)

type contextKey string

const (
	UserKey  contextKey = "user"
	FlashKey contextKey = "flash"
	CSRFKey  contextKey = "csrf"
)

type UserInfo struct {
	ID    int64
	Name  string
	Email string
	Role  string
}

type UserProvider func(ctx context.Context, userID int64) (*UserInfo, error)

func Auth(sessions *services.SessionService, userFn UserProvider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("session")
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			userID, err := sessions.Validate(r.Context(), cookie.Value)
			if err != nil {
				http.SetCookie(w, &http.Cookie{
					Name: "session", Value: "", Path: "/", MaxAge: -1,
				})
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			user, err := userFn(r.Context(), userID)
			if err == nil {
				ctx := context.WithValue(r.Context(), UserKey, user)
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}

func UserFromContext(ctx context.Context) (*UserInfo, bool) {
	u, ok := ctx.Value(UserKey).(*UserInfo)
	return u, ok
}

func Flash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if flash := r.URL.Query().Get("flash"); flash != "" {
			ctx := context.WithValue(r.Context(), FlashKey, flash)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func FlashFromContext(ctx context.Context) (string, bool) {
	f, ok := ctx.Value(FlashKey).(string)
	return f, ok
}

func CSRFToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), CSRFKey, nosurf.Token(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func CSRFFromContext(ctx context.Context) string {
	t, _ := ctx.Value(CSRFKey).(string)
	return t
}
