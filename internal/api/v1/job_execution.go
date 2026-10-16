package v1

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
)

func jobExecutionAPIError(w http.ResponseWriter, err error) {
	var conflict *authorization.JobExecutionConflict
	if errors.As(err, &conflict) {
		status := 409
		if conflict.Code == "invalid_operation_id" || conflict.Code == "invalid_intent" || conflict.Code == "invalid_occurrence" {
			status = 400
		}
		writeError(w, status, conflict.Code, "Review current authorized state; terminal repair requires a new operation ID.")
		return
	}
	commentAPIError(w, err)
}

func (h *handler) jobExecution(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		id, err = req.Service.ResolveSyncedJob(r.Context(), chi.URLParam(r, "id"))
		if err != nil && !errors.Is(err, authorization.ErrNotFound) {
			jobExecutionAPIError(w, err)
			return
		}
	}
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(r.URL.Path, "/history") {
		history, err := req.Service.JobNotesHistory(r.Context(), req.Actor, id)
		if err != nil {
			jobExecutionAPIError(w, err)
			return
		}
		writeJSON(w, 200, history)
		return
	}
	if r.Method == http.MethodGet {
		state, err := req.Service.JobExecution(r.Context(), req.Actor, id)
		if err != nil {
			jobExecutionAPIError(w, err)
			return
		}
		writeJSON(w, 200, state)
		return
	}
	var input struct {
		OperationID string     `json:"operation_id"`
		Revision    string     `json:"notes_revision"`
		Notes       *string    `json:"field_notes"`
		OccurredAt  *time.Time `json:"occurred_at,omitempty"`
	}
	if !decodeExactJSON(w, r, &input, "operation_id", "notes_revision", "field_notes", "occurred_at") {
		return
	}
	in := authorization.JobExecutionCommand{OperationID: input.OperationID, NotesRevision: input.Revision, MissingNotes: input.Notes == nil, OccurredAt: input.OccurredAt}
	if input.Notes != nil {
		in.Notes = *input.Notes
	}
	result, err := req.Service.SubmitJobNotes(r.Context(), req.Actor, id, in)
	if err != nil {
		jobExecutionAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *handler) jobExecutionReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	result, err := req.Service.JobExecutionReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		jobExecutionAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
