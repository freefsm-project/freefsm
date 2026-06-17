package middleware

import (
	"context"
	"net/http"
	"strings"
)

type pathKeyType string
type pageTitleKeyType string

const pathKey pathKeyType = "current_path"
const pageTitleKey pageTitleKeyType = "page_header_title"

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
	if prefix == "/" {
		return path == "/"
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func WithPageHeaderTitle(ctx context.Context, title string) context.Context {
	return context.WithValue(ctx, pageTitleKey, title)
}

func PageHeaderTitleFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(pageTitleKey).(string)
	return t, ok
}
