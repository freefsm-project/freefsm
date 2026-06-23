package handlers

import (
	"net/http"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/middleware"
)

func absoluteAppURL(cfg *config.Config, r *http.Request, path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if cfg != nil && cfg.PublicURL != "" {
		return strings.TrimRight(cfg.PublicURL, "/") + path
	}
	scheme := "http"
	if middleware.IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}
