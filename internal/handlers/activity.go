package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type ActivityHandler struct {
	svc        *services.ActivityService
	userSvc    *services.UserService
	policySvc  *services.PolicyService
	objects    objectref.Directory
	conversion conversionService
}

func NewActivityHandler(svc *services.ActivityService, userSvc *services.UserService, policySvc *services.PolicyService, objects objectref.Directory, conversionServices ...conversionService) *ActivityHandler {
	var conversionSvc conversionService
	if len(conversionServices) > 0 {
		conversionSvc = conversionServices[0]
	}
	return &ActivityHandler{svc: svc, userSvc: userSvc, policySvc: policySvc, objects: objects, conversion: conversionSvc}
}

func (h *ActivityHandler) ListForObject(objectType objectref.Type) http.HandlerFunc {
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
		ref := objectref.New(objectType, objectID)
		if !ref.Valid() {
			http.NotFound(w, r)
			return
		}
		if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyRead) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		entries, err := h.activityForObject(r.Context(), u.ID, u.CompanyID, u.Role, ref)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		rows := h.entriesToRows(r.Context(), entries)
		if rows == nil {
			rows = []templates.ActivityEntry{}
		}

		render(w, r, templates.ActivityWidget(templates.ActivityWidgetData{
			DOMID:   fmt.Sprintf("activity-%s-%d", ref.Type, ref.ID),
			Entries: rows,
		}))
	}
}

func (h *ActivityHandler) activityForObject(ctx context.Context, userID, companyID int64, role string, ref objectref.Ref) ([]services.ActivityEntry, error) {
	if h.conversion != nil && (ref.Type == objectref.TypeEstimate || ref.Type == objectref.TypeInvoice) {
		estimateID := ref.ID
		actor := conversion.Actor{ID: userID, CompanyID: companyID, Role: role}
		if ref.Type == objectref.TypeInvoice {
			eligibility, err := h.conversion.RevertEligibility(ctx, actor, ref.ID)
			if err == nil && eligibility.Active != nil {
				estimateID = eligibility.Active.EstimateID
			} else if err != nil && !errors.Is(err, conversion.ErrNotFound) {
				return nil, err
			}
		}
		if ref.Type == objectref.TypeEstimate || estimateID != ref.ID {
			timeline, err := h.conversion.Timeline(ctx, actor, estimateID)
			if err != nil {
				return nil, err
			}
			entries := make([]services.ActivityEntry, 0, len(timeline))
			for _, entry := range timeline {
				target, _ := objectref.Parse(entry.ObjectType, entry.ObjectID)
				entries = append(entries, services.ActivityEntry{ID: entry.ID, ActorID: entry.ActorID, Action: entry.Action, Target: target, HistoricalTarget: fmt.Sprintf("%s #%d", entry.ObjectType, entry.ObjectID), Metadata: string(entry.Metadata), CreatedAt: entry.CreatedAt})
			}
			return entries, nil
		}
	}
	return h.svc.ListForObject(ctx, companyID, ref, 25)
}

func (h *ActivityHandler) ListByType(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := activityUser(w, r)
		if !ok {
			return
		}
		page := activityPage(r)
		perPage := 10
		offset := (page - 1) * perPage

		entries, total, err := h.svc.ListByType(r.Context(), u.CompanyID, objectType, offset, perPage)
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
	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	page := activityPage(r)
	perPage := 10
	offset := (page - 1) * perPage

	entries, total, err := h.svc.ListByTypes(r.Context(), u.CompanyID, []objectref.Type{objectref.TypeAssetType, objectref.TypeAssetStatus}, offset, perPage)
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

func (h *ActivityHandler) ListSchedule(w http.ResponseWriter, r *http.Request) {
	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	page := activityPage(r)
	perPage := 10
	offset := (page - 1) * perPage

	entries, total, err := h.svc.ListByTypeAndActions(r.Context(), u.CompanyID, objectref.TypeJob, []string{"scheduled", "rescheduled", "dispatched"}, offset, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	totalPages := (total + perPage - 1) / perPage
	rows := h.entriesToRows(r.Context(), entries)
	if rows == nil {
		rows = []templates.ActivityEntry{}
	}

	templates.ActivityRecentList(templates.ActivityPageData{
		Entries:    rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}).Render(r.Context(), w)
}

func (h *ActivityHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	page := activityPage(r)
	perPage := 25
	offset := (page - 1) * perPage

	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	isAdmin := u != nil && u.Role == "admin"

	entries, total, err := h.svc.ListAll(r.Context(), u.CompanyID, offset, perPage, isAdmin)
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

func activityUser(w http.ResponseWriter, r *http.Request) (*middleware.UserInfo, bool) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}

func (h *ActivityHandler) entriesToRows(ctx context.Context, entries []services.ActivityEntry) []templates.ActivityEntry {
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

		entityURL := ""
		if e.Target.Valid() {
			exists, _ := h.objects.Exists(ctx, e.Target, objectref.ExistsAny)
			if url, ok := h.objects.URL(e.Target); exists && ok {
				entityURL = url
			}
		}

		entityName := meta.EntityName
		if entityName == "" && e.Target.Valid() {
			name, err := h.objects.DisplayName(ctx, e.Target)
			if err == nil {
				entityName = name
			}
		}
		if entityName == "" {
			entityName = e.HistoricalTarget
		}
		if entityName == "" && e.Target.Valid() {
			entityName = fmt.Sprintf("%s #%d", e.Target.Type, e.Target.ID)
		}

		rows = append(rows, templates.ActivityEntry{
			ID:         e.ID,
			ActorName:  actorName,
			Action:     e.Action,
			TargetType: string(e.Target.Type),
			EntityName: entityName,
			EntityURL:  entityURL,
			Icon:       activityIcon(e.Action),
			Metadata:   meta,
			CreatedAt:  displayDateTime(ctx, e.CreatedAt),
		})
	}
	return rows
}

func activityPage(r *http.Request) int {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		return 1
	}
	return page
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
	case "scheduled", "rescheduled", "dispatched":
		return "📅"
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
	case "payment_recorded", "payment_deleted":
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
	case "location_created", "location_updated", "location_deleted":
		return "📍"
	default:
		return "●"
	}
}

func (h *ActivityHandler) lookupActorName(ctx context.Context, actorID int64) string {
	if h.userSvc == nil {
		return fmt.Sprintf("User #%d", actorID)
	}
	u, err := h.userSvc.GetByID(ctx, actorID)
	if err != nil || u == nil {
		return fmt.Sprintf("User #%d", actorID)
	}
	return u.Name
}
