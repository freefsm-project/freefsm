package handlers

import (
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"net/http"
)

func pairingPage(s *authorization.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodGet {
			render(w, r, templates.PairingPage(nil, false))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		a := authorizationActor(r)
		if r.URL.Path == "/pair/review" {
			review, err := s.ReviewPairing(r.Context(), a, r.PostForm.Get("code"))
			if err != nil {
				http.Error(w, "Pairing unavailable; start again on your device", 409)
				return
			}
			render(w, r, templates.PairingPage(&review, false))
			return
		}
		if err := s.ApprovePairing(r.Context(), a, r.PostForm.Get("review_token"), r.PostForm.Get("device_name")); err != nil {
			http.Error(w, "Pairing unavailable; review again", 409)
			return
		}
		render(w, r, templates.PairingPage(nil, true))
	}
}
