package middleware

import (
	"context"
	"net/http"
)

type themeKey struct{}

func Theme(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		theme := "light"
		if cookie, err := r.Cookie("theme"); err == nil && cookie.Value == "dark" {
			theme = "dark"
		} else if r.Header.Get("Sec-CH-Prefers-Color-Scheme") == "dark" {
			theme = "dark"
		}
		ctx := context.WithValue(r.Context(), themeKey{}, theme)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ThemeFromContext(ctx context.Context) string {
	t, _ := ctx.Value(themeKey{}).(string)
	if t == "" {
		return "light"
	}
	return t
}
