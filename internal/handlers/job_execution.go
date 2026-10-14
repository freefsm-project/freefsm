package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (h *JobHandler) ExecutionReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	receipt, err := req.Service.JobExecutionReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		jobExecutionHTTPError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(receipt)
}

func optionalJobNotes(r *http.Request) *string {
	if _, ok := r.Form["tech_notes"]; !ok {
		return nil
	}
	value := r.FormValue("tech_notes")
	return &value
}

func jobExecutionHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *authorization.JobExecutionConflict
	if errors.As(err, &conflict) {
		http.Error(w, conflict.Code+": review current authorized values before submitting a new operation", 409)
		return
	}
	jobRelationshipError(w, r, err)
}

func (h *JobHandler) FieldNotes(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/history") {
		history, err := req.Service.JobNotesHistory(r.Context(), req.Actor, id)
		if err != nil {
			jobExecutionHTTPError(w, r, err)
			return
		}
		render(w, r, templates.JobNotesHistory(id, history))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/review") {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if err = req.Service.ReviewJobNotes(r.Context(), req.Actor, id, r.FormValue("baseline_revision"), r.FormValue("reason")); err != nil {
			jobExecutionHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/field-notes", id), 303)
		return
	}
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if len(r.PostForm["field_notes"]) != 1 {
			http.Error(w, "Field Notes text is required", 400)
			return
		}
		_, err = req.Service.SubmitJobNotes(r.Context(), req.Actor, id, authorization.JobExecutionCommand{OperationID: r.FormValue("operation_id"), NotesRevision: r.FormValue("notes_revision"), Notes: r.FormValue("field_notes")})
		if err != nil {
			jobExecutionHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/field-notes?flash=Field+Notes+saved", id), 303)
		return
	}
	state, err := req.Service.JobExecution(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	render(w, r, templates.JobFieldNotes(id, state, uuid.NewString()))
}
