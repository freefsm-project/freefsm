package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

var typeToPrefix = map[string]string{
	"customer": "/customers",
	"job":      "/jobs",
	"project":  "/projects",
	"estimate": "/estimates",
	"invoice":  "/invoices",
	"asset":    "/assets",
}

type CommentHandler struct {
	svc         *services.CommentService
	userSvc     *services.UserService
	activitySvc *services.ActivityService
}

func NewCommentHandler(svc *services.CommentService, userSvc *services.UserService, activitySvc *services.ActivityService) *CommentHandler {
	return &CommentHandler{svc: svc, userSvc: userSvc, activitySvc: activitySvc}
}

func (h *CommentHandler) List(objectType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !canAccessObject(u, objectType, objectID, policyRead) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		comments, err := h.svc.ListForObject(r.Context(), objectType, objectID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		baseURL := fmt.Sprintf("%s/%d", typeToPrefix[objectType], objectID)
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
				CreatedAt: c.CreatedAt.Format("Jan 2, 2006 3:04 PM"),
				CanDelete: canDelete,
			}
		}

		render(w, r, templates.CommentsWidget(templates.CommentsWidgetData{
			BaseURL:    baseURL,
			ObjectType: objectType,
			ObjectID:   objectID,
			Comments:   rows,
		}))
	}
}

func (h *CommentHandler) Create(objectType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !canAccessObject(u, objectType, objectID, policyCreate) {
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

		_, err = h.svc.Create(r.Context(), objectType, objectID, u.ID, content)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		entityName := h.activitySvc.LookupEntityName(r.Context(), objectType, objectID)
		preview := content
		if len(preview) > 100 {
			preview = preview[:100]
		}
		_ = h.activitySvc.Record(r.Context(), u.ID, "comment_added", objectType, objectID, map[string]interface{}{
			"entity_name":     entityName,
			"comment_preview": preview,
		})

		h.List(objectType).ServeHTTP(w, r)
	}
}

func (h *CommentHandler) Delete(objectType string) http.HandlerFunc {
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
		if comment.ObjectType != objectType {
			http.NotFound(w, r)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !canAccessObject(u, objectType, comment.ObjectID, policyDelete) && !(u.ID == comment.AuthorID && canAccessObject(u, objectType, comment.ObjectID, policyRead)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		entityName := h.activitySvc.LookupEntityName(r.Context(), objectType, comment.ObjectID)
		preview := comment.Content
		if len(preview) > 100 {
			preview = preview[:100]
		}

		if err := h.svc.Delete(r.Context(), commentID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		_ = h.activitySvc.Record(r.Context(), u.ID, "comment_deleted", objectType, comment.ObjectID, map[string]interface{}{
			"entity_name":     entityName,
			"comment_preview": preview,
		})

		h.List(objectType).ServeHTTP(w, r)
	}
}
