package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CommentHandler struct {
	svc         *services.CommentService
	userSvc     *services.UserService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	objects     objectref.Directory
}

func NewCommentHandler(svc *services.CommentService, userSvc *services.UserService, activitySvc *services.ActivityService, policySvc *services.PolicyService, objects objectref.Directory) *CommentHandler {
	return &CommentHandler{svc: svc, userSvc: userSvc, activitySvc: activitySvc, policySvc: policySvc, objects: objects}
}

func (h *CommentHandler) List(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ref, err := h.refFromRequest(objectType, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyRead) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		comments, err := h.svc.ListForObject(r.Context(), u.CompanyID, ref)
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}

		baseURL, ok := h.objects.URL(ref)
		if !ok {
			http.NotFound(w, r)
			return
		}
		readOnly := r.URL.Query().Get("read_only") == "1"
		hideTitle := r.URL.Query().Get("hide_title") == "1"
		rows := make([]templates.CommentRow, len(comments))
		authorIDs := make([]int64, len(comments))
		for i, c := range comments {
			authorIDs[i] = c.AuthorID
		}
		authors, err := h.userSvc.ListByIDsForCompany(r.Context(), u.CompanyID, authorIDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		authorNames := make(map[int64]string, len(authors))
		for _, author := range authors {
			authorNames[author.ID] = author.Name
		}
		for i, c := range comments {
			authorName := "Unknown"
			if name, ok := authorNames[c.AuthorID]; ok {
				authorName = name
			}

			canDelete := u.ID == c.AuthorID || isAdminOrDispatcher(u)

			rows[i] = templates.CommentRow{
				ID:        c.ID,
				Author:    authorName,
				Content:   c.Content,
				CreatedAt: displayDateTime(r.Context(), c.CreatedAt),
				CanDelete: canDelete,
			}
		}

		render(w, r, templates.CommentsWidget(templates.CommentsWidgetData{
			BaseURL:    baseURL,
			ObjectType: ref.ObjectType(),
			ObjectID:   ref.ObjectID(),
			Comments:   rows,
			ReadOnly:   readOnly,
			HideTitle:  hideTitle,
		}))
	}
}

func (h *CommentHandler) Create(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ref, err := h.refFromRequest(objectType, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyCreate) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		content := r.FormValue("content")
		if content == "" {
			http.Error(w, "content is required", 400)
			return
		}

		_, err = h.svc.Create(r.Context(), u.CompanyID, ref, u.ID, content)
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}

		entityName := objectDisplayName(r.Context(), h.objects, ref)
		preview := content
		if len(preview) > 100 {
			preview = preview[:100]
		}
		_ = h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "comment_added", ref, map[string]interface{}{
			"entity_name":     entityName,
			"comment_preview": preview,
		})

		h.List(objectType).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) Delete(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ref, err := h.refFromRequest(objectType, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		commentID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		comment, err := h.svc.GetByID(r.Context(), u.CompanyID, commentID)
		if err != nil {
			h.handleServiceError(w, r, err)
			return
		}
		if comment.ObjectType != ref.ObjectType() || comment.ObjectID != ref.ObjectID() {
			http.NotFound(w, r)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyDelete) && !(u.ID == comment.AuthorID && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyCreate)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		entityName := objectDisplayName(r.Context(), h.objects, ref)
		preview := comment.Content
		if len(preview) > 100 {
			preview = preview[:100]
		}

		if err := h.svc.Delete(r.Context(), u.CompanyID, ref, commentID); err != nil {
			h.handleServiceError(w, r, err)
			return
		}

		_ = h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "comment_deleted", ref, map[string]interface{}{
			"entity_name":     entityName,
			"comment_preview": preview,
		})

		h.List(objectType).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) handleServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, services.ErrCommentNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, services.ErrCommentInvalid) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (h *CommentHandler) refFromRequest(objectType objectref.Type, r *http.Request) (objectref.Ref, error) {
	objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return objectref.Ref{}, err
	}
	return h.objects.Parse(string(objectType), objectID)
}
