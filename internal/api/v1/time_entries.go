package v1

import (
	"errors"
	"net/http"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
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

type clockInRequest struct {
	Notes     string   `json:"notes"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

func (h *handler) activeTimeEntry(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r.Context())
	entry, err := h.deps.TimeEntries.GetActiveByUserForCompany(r.Context(), *actor.CompanyID, actor.ID)
	if err != nil {
		if ent.IsNotFound(err) {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load active time entry")
		return
	}
	writeJSON(w, http.StatusOK, makeTimeEntryDTO(entry))
}

func (h *handler) clockIn(w http.ResponseWriter, r *http.Request) {
	job, actor, ok := h.authorizedJob(w, r)
	if !ok {
		return
	}
	if !h.requireOpenJob(w, r, job, *actor.CompanyID) {
		return
	}
	var request clockInRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !validCoordinate(request.Latitude, -90, 90) || !validCoordinate(request.Longitude, -180, 180) {
		writeError(w, http.StatusBadRequest, "invalid_location", "latitude or longitude is out of range")
		return
	}
	entry, err := h.deps.Mutations.ClockIn(r.Context(), mutationActor(actor), job.ID, services.APIClockInParams{Notes: request.Notes, Latitude: request.Latitude, Longitude: request.Longitude})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrMutationForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "job is not assigned to this user")
			return
		case errors.Is(err, services.ErrMutationJobClosed):
			writeError(w, http.StatusConflict, "job_closed", "closed jobs cannot be modified")
			return
		case errors.Is(err, services.ErrMutationJobNotFound):
			writeError(w, http.StatusNotFound, "job_not_found", "job not found")
			return
		case errors.Is(err, services.ErrActiveTimeEntry):
			writeError(w, http.StatusConflict, "already_clocked_in", "user already has an active time entry")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not clock in")
		return
	}
	writeJSON(w, http.StatusCreated, makeMutationTimeEntryDTO(entry))
}

func (h *handler) clockOut(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r.Context())
	entry, err := h.deps.Mutations.ClockOut(r.Context(), mutationActor(actor))
	if err != nil {
		if errors.Is(err, services.ErrTimeEntryNotActive) {
			writeError(w, http.StatusConflict, "not_clocked_in", "user has no active time entry")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not clock out")
		return
	}
	writeJSON(w, http.StatusOK, makeMutationTimeEntryDTO(entry))
}

func makeTimeEntryDTO(entry *ent.TimeEntry) timeEntryDTO {
	return timeEntryDTO{ID: entry.ID, UserID: entry.UserID, JobID: entry.JobID, ClockIn: entry.ClockIn, ClockOut: entry.ClockOut, Notes: entry.Notes, Latitude: entry.Latitude, Longitude: entry.Longitude}
}

func makeMutationTimeEntryDTO(entry services.APITimeEntryResult) timeEntryDTO {
	return timeEntryDTO{ID: entry.ID, UserID: entry.UserID, JobID: entry.JobID, ClockIn: entry.ClockIn, ClockOut: entry.ClockOut, Notes: entry.Notes, Latitude: entry.Latitude, Longitude: entry.Longitude}
}

func mutationActor(actor *ent.User) services.MutationActor {
	return services.MutationActor{CompanyID: *actor.CompanyID, UserID: actor.ID, Name: actor.Name, Role: actor.Role}
}

func validCoordinate(value *float64, min, max float64) bool {
	return value == nil || (*value >= min && *value <= max)
}
