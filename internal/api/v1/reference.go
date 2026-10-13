package v1

import (
	"embed"
	"net/http"
)

//go:embed reference/*
var protocolReference embed.FS

func referenceFile(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := protocolReference.ReadFile("reference/" + name)
		if err != nil {
			writeError(w, 503, "unavailable", "reference unavailable")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	}
}
