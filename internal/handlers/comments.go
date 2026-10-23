package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type CommentHandler struct{ objects objectref.Directory }

func NewCommentHandler(objects objectref.Directory) *CommentHandler {
	return &CommentHandler{objects: objects}
}

func (h *CommentHandler) List(typ objectref.Type) http.HandlerFunc {
	return h.listAfterMutation(typ, nil)
}

func (h *CommentHandler) listAfterMutation(typ objectref.Type, receipt *authorization.CommentReceipt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		ref, err := h.refFromRequest(typ, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		result, err := req.Service.Comments(r.Context(), req.Actor, ref)
		if err != nil {
			if receipt != nil && receipt.Code == "accepted" {
				// The command is already durable. Conversion or access loss during
				// the subsequent widget read cannot turn it into a failed write or
				// silently follow the Comment to its new current document owner.
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("X-FreeFSM-Comment-Operation", receipt.OperationID)
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprintf(w, "Comment saved (operation %s). The document view changed or is unavailable; refresh before another action.", receipt.OperationID)
				return
			}
			h.handleServiceError(w, r, err)
			return
		}
		base, ok := h.objects.URL(ref)
		if !ok {
			http.NotFound(w, r)
			return
		}
		rows := make([]templates.CommentRow, 0, len(result.Entries))
		for _, c := range result.Entries {
			row := templates.CommentRow{ID: c.ID, Author: c.Author, Content: c.Content, CreatedAt: displayDateTime(r.Context(), c.CreatedAt), CanDelete: c.CanRemove, CanReply: c.CanReply, ReplyTo: c.ReplyTo}
			if c.RemovedAt != nil {
				row.Removal = "Removed by " + c.Remover + " · " + displayDateTime(r.Context(), *c.RemovedAt)
			}
			rows = append(rows, row)
		}
		render(w, r, templates.CommentsWidget(templates.CommentsWidgetData{BaseURL: base, ObjectType: string(typ), ObjectID: ref.ID, Comments: rows, CanAdd: result.CanAdd, ReadOnly: r.URL.Query().Get("read_only") == "1", HideTitle: r.URL.Query().Get("hide_title") == "1"}))
	}
}

func (h *CommentHandler) Create(typ objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		ref, err := h.refFromRequest(typ, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		var replyTo int64
		if raw := r.FormValue("reply_to"); raw != "" {
			replyTo, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || replyTo <= 0 {
				http.Error(w, "invalid thread", 400)
				return
			}
		}
		op := r.FormValue("operation_id")
		if op == "" {
			op = uuid.NewString()
		}
		receipt, err := req.Service.SubmitComment(r.Context(), req.Actor, ref, authorization.CommentCommand{OperationID: op, Content: r.FormValue("content"), ReplyTo: replyTo})
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}
		h.listAfterMutation(typ, &receipt).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) Delete(typ objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		ref, err := h.refFromRequest(typ, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		op := r.FormValue("operation_id")
		if op == "" {
			op = uuid.NewString()
		}
		receipt, err := req.Service.SubmitComment(r.Context(), req.Actor, ref, authorization.CommentCommand{OperationID: op, RemoveID: id})
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}
		h.listAfterMutation(typ, &receipt).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) Audit(typ objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		ref, err := h.refFromRequest(typ, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		text, err := req.Service.RetainedComment(r.Context(), req.Actor, ref, id)
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(text))
	}
}

func (h *CommentHandler) handleServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, authorization.ErrCommentInvalid) {
		http.Error(w, err.Error(), 400)
		return
	}
	if !errors.Is(err, authorization.ErrForbidden) && !errors.Is(err, authorization.ErrNotFound) && !errors.Is(err, authorization.ErrConflict) && !errors.Is(err, authorization.ErrPending) {
		http.Error(w, "Comment storage unavailable; retry the same operation", 503)
		return
	}
	authorizationError(w, r, err)
}
func (h *CommentHandler) refFromRequest(typ objectref.Type, r *http.Request) (objectref.Ref, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return objectref.Ref{}, err
	}
	return h.objects.Parse(string(typ), id)
}
