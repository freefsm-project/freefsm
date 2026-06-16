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
}

type CommentHandler struct {
	svc     *services.CommentService
	userSvc *services.UserService
}

func NewCommentHandler(svc *services.CommentService, userSvc *services.UserService) *CommentHandler {
	return &CommentHandler{svc: svc, userSvc: userSvc}
}

func (h *CommentHandler) List(objectType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
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

			canDelete := false
			if u, ok := middleware.UserFromContext(r.Context()); ok && u != nil {
				canDelete = u.ID == c.AuthorID || u.Role == "admin" || u.Role == "dispatcher"
			}

			rows[i] = templates.CommentRow{
				ID:        c.ID,
				Author:    authorName,
				Content:   c.Content,
				CreatedAt: c.CreatedAt.Format("Jan 2, 2006 3:04 PM"),
				CanDelete: canDelete,
			}
		}

		templates.CommentsWidget(templates.CommentsWidgetData{
			BaseURL:    baseURL,
			ObjectType: objectType,
			ObjectID:   objectID,
			Comments:   rows,
		}).Render(r.Context(), w)
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

		if err := h.svc.Delete(r.Context(), commentID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		h.List(objectType).ServeHTTP(w, r)
	}
}
