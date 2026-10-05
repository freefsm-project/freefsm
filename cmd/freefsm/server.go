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
	"github.com/justinas/nosurf"
)

type instanceRoutes struct {
	control *instancecontrol.Control
	refresh func() error
	backup  http.Handler
}

func newApplicationHandler(apiHandler, webHandler http.Handler, instances ...instanceRoutes) http.Handler {
	webCSRF := nosurf.New(webHandler)
	webCSRF.SetIsTLSFunc(func(r *http.Request) bool {
		return appmw.IsHTTPS(r)
	})

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
		admitted = instance.control.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := instance.refresh(); err != nil {
				http.Error(w, "Instance connections unavailable", 503)
				return
			}
			normal.ServeHTTP(w, r)
		}))
	}
	logged := chimw.Logger(admitted)
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
