package v1

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

type timeEntryDTO struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	JobID     *int64     `json:"job_id"`
	ClockIn   time.Time  `json:"clock_in"`
	ClockOut  *time.Time `json:"clock_out"`
	Notes     string     `json:"notes"`
	Latitude  *float64   `json:"latitude"`
	Longitude *float64   `json:"longitude"`
}

func clockAPIError(w http.ResponseWriter, err error) {
	status, code := 503, "retryable_failure"
	var conflict *authorization.ClockConflict
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, authorization.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.As(err, &conflict):
		status, code = 409, conflict.Code
		if strings.HasPrefix(code, "invalid_") {
			status = 400
		}
	}
	slog.Warn("clock request rejected", "status", status, "code", code)
	if status >= 500 {
		w.Header().Set("Retry-After", "5")
	}
	writeError(w, status, code, code)
}
func (h *handler) activeTimeEntry(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		clockAPIError(w, authorization.ErrUnavailable)
		return
	}
	result, err := req.Service.ActiveClock(r.Context(), req.Actor)
	if err != nil {
		clockAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *handler) clockIn(w http.ResponseWriter, r *http.Request)  { h.submitClock(w, r, false) }
func (h *handler) clockOut(w http.ResponseWriter, r *http.Request) { h.submitClock(w, r, true) }
func (h *handler) submitClock(w http.ResponseWriter, r *http.Request, out bool) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Clock commands do not accept query parameters")
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		clockAPIError(w, authorization.ErrUnavailable)
		return
	}
	var wire struct {
		authorization.ClockCommand
		JobID *int64 `json:"job_id"`
	}
	if !decodeJSON(w, r, &wire) {
		return
	}
	in := wire.ClockCommand
	if wire.JobID != nil {
		if *wire.JobID <= 0 {
			clockAPIError(w, &authorization.ClockConflict{Code: "invalid_job"})
			return
		}
		in.JobID = *wire.JobID
	}
	if id := chi.URLParam(r, "id"); id != "" {
		jobID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || jobID <= 0 {
			clockAPIError(w, authorization.ErrNotFound)
			return
		}
		if in.JobID != 0 && in.JobID != jobID {
			clockAPIError(w, &authorization.ClockConflict{Code: "invalid_job"})
			return
		}
		in.JobID = jobID
	}
	result, err := req.Service.SubmitClock(r.Context(), req.Actor, out, in)
	if err != nil {
		clockAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *handler) clockLease(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		clockAPIError(w, authorization.ErrUnavailable)
		return
	}
	var in struct{}
	if !decodeJSON(w, r, &in) {
		return
	}
	result, err := req.Service.ClockLease(r.Context(), req.Actor)
	if err != nil {
		clockAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *handler) clockReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		clockAPIError(w, authorization.ErrUnavailable)
		return
	}
	result, err := req.Service.ClockReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		clockAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func mutationActor(actor *ent.User) services.MutationActor {
	return services.MutationActor{CompanyID: *actor.CompanyID, UserID: actor.ID, Name: actor.Name, Role: actor.Role}
}
func validCoordinate(value *float64, min, max float64) bool {
	return value == nil || *value >= min && *value <= max
}
