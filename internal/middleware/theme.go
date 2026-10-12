package middleware

import (
	"context"
	"net/http"
)

type themeKey struct{}

func Theme(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		theme := "system"
		if cookie, err := r.Cookie("theme"); err == nil && (cookie.Value == "dark" || cookie.Value == "light") {
			theme = cookie.Value
		}
		ctx := context.WithValue(r.Context(), themeKey{}, theme)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ThemeFromContext(ctx context.Context) string {
	t, _ := ctx.Value(themeKey{}).(string)
	if t == "" {
		return "system"
	}
	return t
}
