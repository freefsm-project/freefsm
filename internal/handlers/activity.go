package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

var activityTypeToPrefix = map[string]string{
	"customer":         "/customers",
	"job":              "/jobs",
	"project":          "/projects",
	"estimate":         "/estimates",
	"invoice":          "/invoices",
	"asset":            "/assets",
	"item":             "/items",
	"time_entry":       "/time-entries",
	"user":             "/users",
	"tag":              "/tags",
	"asset_type":       "/settings/assets",
	"asset_status":     "/settings/assets",
	"company_settings": "/settings",
	"custom_field":     "/settings/custom-fields",
}

type ActivityHandler struct {
	svc       *services.ActivityService
	userSvc   *services.UserService
	policySvc *services.PolicyService
}

func NewActivityHandler(svc *services.ActivityService, userSvc *services.UserService, policySvc *services.PolicyService) *ActivityHandler {
	return &ActivityHandler{svc: svc, userSvc: userSvc, policySvc: policySvc}
}

func (h *ActivityHandler) ListForObject(objectType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectType, objectID, policyRead) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		entries, err := h.svc.ListForObject(r.Context(), objectType, objectID, 25)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		rows := h.entriesToRows(r.Context(), entries)
		if rows == nil {
			rows = []templates.ActivityEntry{}
		}

		render(w, r, templates.ActivityWidget(templates.ActivityWidgetData{
			ObjectType: objectType,
			ObjectID:   objectID,
			Entries:    rows,
		}))
	}
}

func (h *ActivityHandler) ListByType(objectType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		perPage := 10
		offset := (page - 1) * perPage

		entries, total, err := h.svc.ListByType(r.Context(), objectType, offset, perPage)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		totalPages := (total + perPage - 1) / perPage
		rows := h.entriesToRows(r.Context(), entries)
		if rows == nil {
			rows = []templates.ActivityEntry{}
		}

		data := templates.ActivityPageData{
			Entries:    rows,
			Page:       page,
			PerPage:    perPage,
			Total:      total,
			TotalPages: totalPages,
		}

		if r.Header.Get("HX-Request") == "true" {
			templates.ActivityRecentList(data).Render(r.Context(), w)
			return
		}
		templates.ActivityIndex(data).Render(r.Context(), w)
	}
}

func (h *ActivityHandler) ListForAssetSettings(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	perPage := 10
	offset := (page - 1) * perPage

	entries, total, err := h.svc.ListByTypes(r.Context(), []string{"asset_type", "asset_status"}, offset, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	totalPages := (total + perPage - 1) / perPage
	rows := h.entriesToRows(r.Context(), entries)
	if rows == nil {
		rows = []templates.ActivityEntry{}
	}

	data := templates.ActivityPageData{
		Entries:    rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}

	if r.Header.Get("HX-Request") == "true" {
		templates.ActivityRecentList(data).Render(r.Context(), w)
		return
	}
	templates.ActivityIndex(data).Render(r.Context(), w)
}

func (h *ActivityHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	perPage := 25
	offset := (page - 1) * perPage

	u, _ := middleware.UserFromContext(r.Context())
	isAdmin := u != nil && u.Role == "admin"

	entries, total, err := h.svc.ListAll(r.Context(), offset, perPage, isAdmin)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	totalPages := (total + perPage - 1) / perPage
	rows := h.entriesToRows(r.Context(), entries)
	if rows == nil {
		rows = []templates.ActivityEntry{}
	}

	templates.ActivityIndex(templates.ActivityPageData{
		Entries:    rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}).Render(r.Context(), w)
}

func (h *ActivityHandler) entriesToRows(ctx context.Context, entries []*ent.ActivityLog) []templates.ActivityEntry {
	rows := make([]templates.ActivityEntry, 0, len(entries))
	for _, e := range entries {
		var meta templates.ActivityMetadata
		if err := json.Unmarshal([]byte(e.Metadata), &meta); err != nil {
			meta = templates.ActivityMetadata{}
		}

		actorName := meta.ActorName
		if actorName == "" {
			actorName = h.lookupActorName(ctx, e.ActorID)
		}

		entityURL := buildEntityURL(e.ObjectType, e.ObjectID)

		entityName := meta.EntityName
		if entityName == "" {
			entityName = h.svc.LookupEntityName(ctx, e.ObjectType, e.ObjectID)
		}

		rows = append(rows, templates.ActivityEntry{
			ID:           e.ID,
			ActorName:    actorName,
			Action:       e.Action,
			ObjectType:   e.ObjectType,
			ObjectID:     e.ObjectID,
			EntityName:   entityName,
			EntityURL:    entityURL,
			Icon:         activityIcon(e.Action),
			Metadata:     meta,
			RelativeTime: services.ActivityRelativeTime(e.CreatedAt),
			CreatedAt:    e.CreatedAt.Format("Jan 2, 2006 3:04 PM"),
		})
	}
	return rows
}

func activityIcon(action string) string {
	switch action {
	case "created", "type_created", "status_created", "field_created", "tag_created", "user_created":
		return "+"
	case "updated", "type_updated", "status_updated", "field_updated", "tag_updated", "user_updated", "settings_updated":
		return "✎"
	case "deleted", "type_deleted", "status_deleted", "field_deleted", "tag_deleted", "contact_deleted":
		return "🗑"
	case "archived":
		return "📦"
	case "status_changed":
		return "↻"
	case "tag_attached", "tag_detached":
		return "#"
	case "file_uploaded", "file_deleted":
		return "📎"
	case "comment_added", "comment_deleted":
		return "✍"
	case "clocked_in":
		return "▶"
	case "clocked_out":
		return "■"
	case "payment_recorded":
		return "$"
	case "user_disabled":
		return "🚫"
	case "user_enabled":
		return "✔"
	case "password_reset", "password_changed":
		return "🔒"
	case "welcome_resent":
		return "📧"
	case "subtask_completed":
		return "☑"
	case "subtask_uncompleted":
		return "☐"
	case "logged_in":
		return "🔑"
	case "logged_out":
		return "🚪"
	case "logo_uploaded":
		return "🖼"
	case "restored":
		return "↩"
	case "contact_created", "contact_updated":
		return "👤"
	default:
		return "●"
	}
}

func buildEntityURL(objectType string, objectID int64) string {
	switch objectType {
	case "asset_type", "asset_status":
		return "/settings/assets"
	case "company_settings":
		return "/settings"
	case "custom_field":
		return "/settings/custom-fields"
	case "tag":
		return fmt.Sprintf("/tags/%d", objectID)
	case "user":
		return fmt.Sprintf("/users/%d", objectID)
	default:
		prefix, ok := activityTypeToPrefix[objectType]
		if !ok {
			prefix = "/" + objectType + "s"
		}
		return fmt.Sprintf("%s/%d", prefix, objectID)
	}
}

func (h *ActivityHandler) lookupActorName(ctx context.Context, actorID int64) string {
	u, err := h.userSvc.GetByID(ctx, actorID)
	if err != nil || u == nil {
		return fmt.Sprintf("User #%d", actorID)
	}
	return u.Name
}
