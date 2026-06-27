package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type TimeEntryHandler struct {
	svc         *services.TimeEntryService
	userSvc     *services.UserService
	activitySvc *services.ActivityService
}

func NewTimeEntryHandler(svc *services.TimeEntryService, userSvc *services.UserService, activitySvc *services.ActivityService) *TimeEntryHandler {
	return &TimeEntryHandler{svc: svc, userSvc: userSvc, activitySvc: activitySvc}
}

func (h *TimeEntryHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25

	user, _ := middleware.UserFromContext(r.Context())
	isAdmin := user != nil && user.Role == "admin"
	isDispatcher := user != nil && user.Role == "dispatcher"
	canViewAll := isAdmin || isDispatcher

	var filterUserID int64
	if canViewAll {
		filterUserID, _ = strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	} else if user != nil {
		filterUserID = user.ID
	}

	search := r.URL.Query().Get("search")

	entries, total, err := h.svc.List(r.Context(), filterUserID, search, page, perPage, canViewAll)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	userIDs := make(map[int64]struct{})
	for _, e := range entries {
		userIDs[e.UserID] = struct{}{}
	}
	userNames := make(map[int64]string)
	for uid := range userIDs {
		if u, err := h.userSvc.GetByID(r.Context(), uid); err == nil {
			userNames[uid] = u.Name
		}
	}

	loc := middleware.CompanyLocation(r.Context())
	rows := make([]templates.TimeEntryRow, len(entries))
	for i, e := range entries {
		rows[i] = timeEntryToRow(e, userNames, user, loc)
	}

	var users []templates.UserRow
	if canViewAll {
		allUsers, _ := h.userSvc.ListAll(r.Context())
		users = make([]templates.UserRow, len(allUsers))
		for i, u := range allUsers {
			users[i] = templates.UserRow{ID: u.ID, Name: u.Name}
		}
	}

	data := templates.TimeEntryListPageData{
		Entries:        rows,
		Page:           page,
		PerPage:        perPage,
		Total:          total,
		TotalPages:     services.TimeEntryPaginationTotalPages(total, perPage),
		Search:         search,
		ShowUserFilter: canViewAll,
		Users:          users,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		render(w, r, templates.TimeEntriesTable(data))
		return
	}
	render(w, r, templates.TimeEntriesIndex(data))
}

func (h *TimeEntryHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	entry, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	currentUser, ok := middleware.UserFromContext(r.Context())
	if !ok || currentUser == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !isAdminOrDispatcher(currentUser) && entry.UserID != currentUser.ID {
		http.Error(w, "forbidden", 403)
		return
	}

	userName := "Unknown"
	if u, err := h.userSvc.GetByID(r.Context(), entry.UserID); err == nil {
		userName = u.Name
	}

	loc := middleware.CompanyLocation(r.Context())
	clockInStr := entry.ClockIn.In(loc).Format("Jan 2, 2006 3:04 PM")
	clockOutStr := ""
	if entry.ClockOut != nil {
		clockOutStr = entry.ClockOut.In(loc).Format("Jan 2, 2006 3:04 PM")
	}
	duration := services.TimeEntryDuration(entry.ClockIn, safeTime(entry.ClockOut))

	gpsLat := ""
	gpsLon := ""
	if entry.Latitude != nil {
		gpsLat = fmt.Sprintf("%.6f", *entry.Latitude)
	}
	if entry.Longitude != nil {
		gpsLon = fmt.Sprintf("%.6f", *entry.Longitude)
	}

	data := templates.TimeEntryShowPageData{
		ID:       entry.ID,
		UserName: userName,
		ClockIn:  clockInStr,
		ClockOut: clockOutStr,
		Duration: duration,
		IsManual: entry.IsManual,
		Notes:    entry.Notes,
		GPSLat:   gpsLat,
		GPSLon:   gpsLon,
	}
	render(w, r, templates.TimeEntryShow(data))
}

func (h *TimeEntryHandler) ClockIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	user, _ := middleware.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	result, err := h.svc.ClockIn(r.Context(), services.TimeEntryCreateParams{
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, services.ErrActiveTimeEntry) {
			http.Redirect(w, r, "/?flash=You+are+already+clocked+in", http.StatusSeeOther)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	loc := middleware.CompanyLocation(r.Context())
	clockInStr := result.ClockIn.In(loc).Format("Jan 2 3:04 PM")
	h.activitySvc.Record(r.Context(), user.ID, "clocked_in", "time_entry", result.ID, map[string]interface{}{
		"entity_name": fmt.Sprintf("%s — %s", user.Name, clockInStr),
		"actor_name":  user.Name,
	})

	http.Redirect(w, r, "/?flash=Clocked+in", http.StatusSeeOther)
}

func (h *TimeEntryHandler) ClockOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	user, _ := middleware.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}

	activeEntry, err := h.svc.GetActiveByUser(r.Context(), user.ID)
	if err != nil {
		http.Redirect(w, r, "/?flash=No+active+clock+in+found", http.StatusSeeOther)
		return
	}

	result, err := h.svc.ClockOut(r.Context(), activeEntry.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	loc := middleware.CompanyLocation(r.Context())
	duration := services.TimeEntryDuration(result.ClockIn, safeTime(result.ClockOut))
	clockInStr := result.ClockIn.In(loc).Format("Jan 2 3:04 PM")
	h.activitySvc.Record(r.Context(), user.ID, "clocked_out", "time_entry", result.ID, map[string]interface{}{
		"entity_name": fmt.Sprintf("%s — %s (%s)", user.Name, clockInStr, duration),
		"actor_name":  user.Name,
	})

	http.Redirect(w, r, "/?flash=Clocked+out", http.StatusSeeOther)
}

func (h *TimeEntryHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	user, _ := middleware.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}

	entry, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	canEdit := isAdminOrDispatcher(user) || entry.UserID == user.ID
	if !canEdit {
		http.Error(w, "forbidden", 403)
		return
	}

	if r.Method == http.MethodGet {
		loc := middleware.CompanyLocation(r.Context())
		clockOutStr := ""
		if entry.ClockOut != nil {
			clockOutStr = entry.ClockOut.In(loc).Format("2006-01-02T15:04:05")
		}

		data := templates.TimeEntryFormPageData{
			Entry: &templates.TimeEntryFormEntry{
				ID:       entry.ID,
				ClockIn:  entry.ClockIn.In(loc).Format("2006-01-02T15:04:05"),
				ClockOut: clockOutStr,
				Notes:    entry.Notes,
			},
		}
		render(w, r, templates.TimeEntryForm(data))
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	params := services.TimeEntryUpdateParams{
		IsManual: boolPtr(true),
	}
	loc := middleware.CompanyLocation(r.Context())
	if clockIn := r.FormValue("clock_in"); clockIn != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", clockIn, loc); err == nil {
			params.ClockIn = &t
		}
	}
	if clockOut := r.FormValue("clock_out"); clockOut != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", clockOut, loc); err == nil {
			params.ClockOut = &t
		}
	}
	params.Notes = formPtr(r.FormValue("notes"))

	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	loc = middleware.CompanyLocation(r.Context())
	clockInStr := result.ClockIn.In(loc).Format("Jan 2 3:04 PM")
	entityName := fmt.Sprintf("%s — %s", user.Name, clockInStr)
	if result.ClockOut != nil {
		entityName += fmt.Sprintf(" — %s", result.ClockOut.In(loc).Format("3:04 PM"))
	}
	h.activitySvc.Record(r.Context(), user.ID, "updated", "time_entry", id, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  user.Name,
	})

	http.Redirect(w, r, "/time-entries?flash=Time+entry+updated", http.StatusSeeOther)
}

func (h *TimeEntryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}

	user, _ := middleware.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}

	entry, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	canDelete := isAdminOrDispatcher(user) || entry.UserID == user.ID
	if !canDelete {
		http.Error(w, "forbidden", 403)
		return
	}

	loc := middleware.CompanyLocation(r.Context())
	clockInStr := entry.ClockIn.In(loc).Format("Jan 2 3:04 PM")
	entityName := clockInStr
	if entry.ClockOut != nil {
		entityName += " — " + entry.ClockOut.In(loc).Format("3:04 PM")
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.activitySvc.Record(r.Context(), user.ID, "deleted", "time_entry", id, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  user.Name,
	})

	http.Redirect(w, r, "/time-entries?flash=Time+entry+deleted", http.StatusSeeOther)
}

func timeEntryToRow(e *ent.TimeEntry, userNames map[int64]string, currentUser *middleware.UserInfo, loc *time.Location) templates.TimeEntryRow {
	clockIn := e.ClockIn.In(loc).Format("Jan 2, 2006 3:04 PM")
	clockOut := ""
	if e.ClockOut != nil {
		clockOut = e.ClockOut.In(loc).Format("Jan 2, 2006 3:04 PM")
	}

	duration := services.TimeEntryDuration(e.ClockIn, safeTime(e.ClockOut))

	userName := userNames[e.UserID]

	isAdmin := currentUser != nil && currentUser.Role == "admin"
	canEdit := isAdmin || (currentUser != nil && currentUser.ID == e.UserID)

	return templates.TimeEntryRow{
		ID:       e.ID,
		UserName: userName,
		IsManual: e.IsManual,
		ClockIn:  clockIn,
		ClockOut: clockOut,
		Duration: duration,
		Notes:    e.Notes,
		CanEdit:  canEdit,
	}
}

func safeTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
