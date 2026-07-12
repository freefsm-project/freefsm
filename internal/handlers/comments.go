package handlers

import (
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
		ref, err := h.refFromRequest(objectType, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyRead) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		comments, err := h.svc.ListForObject(r.Context(), ref)
		if err != nil {
			http.Error(w, err.Error(), 500)
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
		for i, c := range comments {
			author, _ := h.userSvc.GetByID(r.Context(), c.AuthorID)
			authorName := "Unknown"
			if author != nil {
				authorName = author.Name
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
		ref, err := h.refFromRequest(objectType, r)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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

		_, err = h.svc.Create(r.Context(), ref, u.ID, content)
		if err != nil {
			http.Error(w, err.Error(), 500)
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
		commentID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		comment, err := h.svc.GetByID(r.Context(), commentID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ref, err := h.objects.Parse(string(objectType), comment.ObjectID)
		if err != nil || comment.ObjectType != ref.ObjectType() {
			http.NotFound(w, r)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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

		if err := h.svc.Delete(r.Context(), commentID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		_ = h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "comment_deleted", ref, map[string]interface{}{
			"entity_name":     entityName,
			"comment_preview": preview,
		})

		h.List(objectType).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) refFromRequest(objectType objectref.Type, r *http.Request) (objectref.Ref, error) {
	objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return objectref.Ref{}, err
	}
	return h.objects.Parse(string(objectType), objectID)
}
