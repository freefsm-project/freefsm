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
)

// Checklist commands own exact-identity receipts and facet-specific revision
// checks. A generic whole-Job revision would incorrectly reject completion
// after an unrelated rename/reorder, and create a second receipt namespace.
func subtaskProtocolPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 && !(len(parts) == 4 && parts[3] == "review") {
		return false
	}
	if parts[0] != "jobs" || parts[2] != "subtasks" {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && id > 0
}

func (h *JobHandler) Subtasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == "POST" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		in := authorization.SubtaskCommand{OperationID: r.FormValue("operation_id"), Action: r.FormValue("action"), SubtaskID: r.FormValue("subtask_id"), Title: r.FormValue("title"), CompletionRevision: r.FormValue("completion_revision"), Order: r.Form["order"]}
		result, e := req.Service.SubmitSubtask(r.Context(), req.Actor, id, in)
		err = e
		if err != nil {
			subtaskHTTPError(w, r, err)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		http.Redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10)+"/subtasks", 303)
		return
	}
	result, err := req.Service.Subtasks(r.Context(), req.Actor, id)
	if err != nil {
		subtaskHTTPError(w, r, err)
		return
	}
	var review *authorization.SubtaskReview
	if result.ReviewRequired {
		if v, err := req.Service.SubtaskReview(r.Context(), req.Actor, id); err == nil {
			review = &v
		}
	}
	r = withDetailPresentation(r, "job", id, h.policySvc)
	render(w, r, templates.SubtaskPage(id, result, review))
}

func (h *JobHandler) ReviewSubtasks(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := req.Service.ReviewSubtasks(r.Context(), req.Actor, id, r.FormValue("snapshot"), r.FormValue("acknowledge") == "true"); err != nil {
		subtaskHTTPError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d/subtasks", id), 303)
}
func (h *JobHandler) SubtaskReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	result, err := req.Service.SubtaskReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		subtaskHTTPError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}
func subtaskHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, authorization.ErrForbidden):
		http.Error(w, "Forbidden", 403)
	case errors.Is(err, authorization.ErrConflict), errors.Is(err, authorization.ErrSubtaskReview):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, authorization.ErrSubtaskInvalid):
		http.Error(w, err.Error(), 400)
	default:
		internalServerError(w, r, "Subtask action", err)
	}
}
