package delivery

import (
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Service) OpenHandler(w http.ResponseWriter, r *http.Request) {
	for k, v := range TrackingResponseHeaders() {
		w.Header().Set(k, v)
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := chi.URLParam(r, "token")
	_ = s.RecordOpen(r.Context(), token) // Never reveal token validity.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(TrackingPixelGIF())
}
