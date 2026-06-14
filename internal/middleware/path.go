package middleware

import (
	"context"
	"net/http"
	"strings"
)

type pathKeyType string

const pathKey pathKeyType = "current_path"

func CurrentPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), pathKey, r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func PathFromContext(ctx context.Context) string {
	p, _ := ctx.Value(pathKey).(string)
	return p
}

func IsActivePath(ctx context.Context, prefix string) bool {
	path := PathFromContext(ctx)
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
