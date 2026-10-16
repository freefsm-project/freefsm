package v1

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/go-chi/chi/v5"
)

func commentAPIError(w http.ResponseWriter, err error) {
	status, code := 503, "retryable_failure"
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, authorization.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, authorization.ErrConflict):
		status, code = 409, "comment_conflict"
	case errors.Is(err, authorization.ErrCommentInvalid):
		status, code = 400, "invalid_comment"
	case errors.Is(err, authorization.ErrCommentParentChanged):
		status, code = 409, "comment_parent_changed"
	}
	writeError(w, status, code, code)
}
func (h *handler) comments(typ objectref.Type, remove bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			writeError(w, 503, "unavailable", "authorization unavailable")
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "not found")
			return
		}
		ref := objectref.New(typ, id)
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_request", "Comment requests have no query parameters")
			return
		}
		if r.Method == http.MethodGet {
			result, err := req.Service.Comments(r.Context(), req.Actor, ref)
			if err != nil {
				commentAPIError(w, err)
				return
			}
			writeJSON(w, 200, result)
			return
		}
		var in authorization.CommentCommand
		properties := []string{"operation_id", "occurred_at", "parent_revision"}
		if !remove {
			properties = append(properties, "content", "reply_to")
		}
		if !decodeExactJSON(w, r, &in, properties...) {
			return
		}
		if remove {
			in.RemoveID, err = strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
			if err != nil || in.RemoveID <= 0 || in.Content != "" || in.ReplyTo != 0 {
				writeError(w, 400, "invalid_comment", "invalid removal")
				return
			}
		}
		result, err := req.Service.SubmitComment(r.Context(), req.Actor, ref, in)
		if err != nil {
			commentAPIError(w, err)
			return
		}
		writeJSON(w, 200, result)
	}
}
func (h *handler) commentReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	result, err := req.Service.CommentReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		commentAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
