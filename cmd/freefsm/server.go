package main

import (
	"net/http"
	"strings"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	appmw "github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type instanceRoutes struct {
	control *instancecontrol.Control
	refresh func() error
	backup  http.Handler
}

func newApplicationHandler(apiHandler, webHandler http.Handler, instances ...instanceRoutes) http.Handler {
	webCSRF := appmw.BrowserCSRF(webHandler)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(apiv1.CaptureTransportPeer)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	normal := chi.NewRouter()
	normal.Mount("/api/v1", apiHandler)
	normal.Mount("/", webCSRF)
	var admitted http.Handler = normal
	if len(instances) > 0 {
		instance := instances[0]
		admitted = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, release, err := instance.control.Enter(r.Context())
			if err != nil {
				writeAdmissionFailure(w, r, instancecontrol.ErrMaintenance.Error())
				return
			}
			defer release()
			if err := instance.refresh(); err != nil {
				writeAdmissionFailure(w, r, "Instance connections unavailable")
				return
			}
			normal.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	logged := queryRedactedRequestLogger(admitted)
	r.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(instances) > 0 && (r.URL.Path == "/settings/backup" || strings.HasPrefix(r.URL.Path, "/settings/backup/")) {
			// No URL/body/header logging on the capability-bearing control surface.
			instances[0].backup.ServeHTTP(w, r)
			return
		}
		logged.ServeHTTP(w, r)
	}))
	return r
}

func writeAdmissionFailure(w http.ResponseWriter, r *http.Request, browserMessage string) {
	if r.URL.Path == "/api/v1" || strings.HasPrefix(r.URL.Path, "/api/v1/") {
		apiv1.WriteUnavailable(w)
		return
	}
	w.Header().Set("Retry-After", "5")
	http.Error(w, browserMessage, http.StatusServiceUnavailable)
}

// Request queries can contain invitation/recovery capabilities. Give the logger
// a redacted snapshot while preserving the original URL for request handling.
func queryRedactedRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logged := r.Clone(r.Context())
		logged.URL.RawQuery = ""
		logged.URL.ForceQuery = false
		logged.RequestURI = logged.URL.EscapedPath()
		chimw.Logger(http.HandlerFunc(func(w http.ResponseWriter, logRequest *http.Request) {
			next.ServeHTTP(w, r.WithContext(logRequest.Context()))
		})).ServeHTTP(w, logged)
	})
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
