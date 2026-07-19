package main

import (
	"net/http"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	appmw "github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"
)

func newApplicationHandler(apiHandler, webHandler http.Handler) http.Handler {
	webCSRF := nosurf.New(webHandler)
	webCSRF.SetIsTLSFunc(func(r *http.Request) bool {
		return appmw.IsHTTPS(r)
	})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(apiv1.CaptureTransportPeer)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Mount("/api/v1", apiHandler)
	r.Mount("/", webCSRF)
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
