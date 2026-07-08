package handlers

import (
	"context"
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
	jobSvc      *services.JobService
	activitySvc *services.ActivityService
}

func NewTimeEntryHandler(svc *services.TimeEntryService, userSvc *services.UserService, jobSvc *services.JobService, activitySvc *services.ActivityService) *TimeEntryHandler {
	return &TimeEntryHandler{svc: svc, userSvc: userSvc, jobSvc: jobSvc, activitySvc: activitySvc}
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
	jobNames := h.jobNames(r.Context(), entries)

	rows := make([]templates.TimeEntryRow, len(entries))
	for i, e := range entries {
		rows[i] = timeEntryToRow(r.Context(), e, userNames, jobNames, user)
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
	jobID, jobName := h.timeEntryJob(r.Context(), entry)

	clockInStr := displayDateTime(r.Context(), entry.ClockIn)
	clockOutStr := ""
	if entry.ClockOut != nil {
		clockOutStr = displayDateTime(r.Context(), *entry.ClockOut)
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
		JobID:    jobID,
		JobName:  jobName,
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
	clockInStr := displayDateTime(r.Context(), result.ClockIn)
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
	duration := services.TimeEntryDuration(result.ClockIn, safeTime(result.ClockOut))
	clockInStr := displayDateTime(r.Context(), result.ClockIn)
	entityName := fmt.Sprintf("%s — %s (%s)", user.Name, clockInStr, duration)
	if result.JobID != nil && *result.JobID > 0 {
		if jobID, jobName := h.timeEntryJob(r.Context(), result); jobID > 0 && jobName != "" {
			entityName = fmt.Sprintf("%s — %s — %s (%s)", user.Name, jobName, clockInStr, duration)
			h.activitySvc.Record(r.Context(), user.ID, "clocked_out", "job", jobID, map[string]interface{}{
				"entity_name":   fmt.Sprintf("%s — %s (%s)", jobName, clockInStr, duration),
				"actor_name":    user.Name,
				"time_entry_id": result.ID,
				"clock_in":      clockInStr,
				"duration":      duration,
			})
		}
	}
	h.activitySvc.Record(r.Context(), user.ID, "clocked_out", "time_entry", result.ID, map[string]interface{}{
		"entity_name":   entityName,
		"actor_name":    user.Name,
		"time_entry_id": result.ID,
		"clock_in":      clockInStr,
		"duration":      duration,
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
		jobs, err := h.allowedJobsForUser(r.Context(), user)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		loc := middleware.CompanyLocation(r.Context())
		clockOutStr := ""
		if entry.ClockOut != nil {
			clockOutStr = entry.ClockOut.In(loc).Format("2006-01-02T15:04:05")
		}

		data := templates.TimeEntryFormPageData{
			Entry: &templates.TimeEntryFormEntry{
				ID:       entry.ID,
				JobID:    valueInt64(entry.JobID),
				ClockIn:  entry.ClockIn.In(loc).Format("2006-01-02T15:04:05"),
				ClockOut: clockOutStr,
				Notes:    entry.Notes,
			},
			Jobs: jobOptions(jobs),
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
	jobs, err := h.allowedJobsForUser(r.Context(), user)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jobID, err := parseOptionalID(r.FormValue("job_id"))
	if err != nil {
		http.Error(w, "invalid job", 400)
		return
	}
	if jobID > 0 {
		if !jobAllowed(jobs, jobID) {
			http.Error(w, "job not allowed", 400)
			return
		}
		params.JobID = &jobID
	} else {
		params.ClearJob = true
	}

	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	clockInStr := displayDateTime(r.Context(), result.ClockIn)
	entityName := fmt.Sprintf("%s — %s", user.Name, clockInStr)
	if result.ClockOut != nil {
		entityName += fmt.Sprintf(" — %s", displayTime(r.Context(), *result.ClockOut))
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

	clockInStr := displayDateTime(r.Context(), entry.ClockIn)
	entityName := clockInStr
	if entry.ClockOut != nil {
		entityName += " — " + displayTime(r.Context(), *entry.ClockOut)
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

func (h *TimeEntryHandler) jobNames(ctx context.Context, entries []*ent.TimeEntry) map[int64]string {
	names := make(map[int64]string)
	for _, e := range entries {
		if e.JobID == nil || *e.JobID == 0 {
			continue
		}
		if _, ok := names[*e.JobID]; ok {
			continue
		}
		if j, err := h.jobSvc.GetByID(ctx, *e.JobID); err == nil && j != nil {
			names[*e.JobID] = jobDisplayName(j)
		}
	}
	return names
}

func (h *TimeEntryHandler) timeEntryJob(ctx context.Context, e *ent.TimeEntry) (int64, string) {
	if e.JobID == nil || *e.JobID == 0 {
		return 0, ""
	}
	if j, err := h.jobSvc.GetByID(ctx, *e.JobID); err == nil && j != nil {
		return *e.JobID, jobDisplayName(j)
	}
	return *e.JobID, fmt.Sprintf("Job #%d", *e.JobID)
}

func (h *TimeEntryHandler) allowedJobsForUser(ctx context.Context, user *middleware.UserInfo) ([]*ent.Job, error) {
	if isAdminOrDispatcher(user) {
		return h.jobSvc.ListAll(ctx)
	}
	return h.jobSvc.ListAssignedAll(ctx, user.ID)
}

func timeEntryToRow(ctx context.Context, e *ent.TimeEntry, userNames map[int64]string, jobNames map[int64]string, currentUser *middleware.UserInfo) templates.TimeEntryRow {
	clockIn := displayDateTime(ctx, e.ClockIn)
	clockOut := ""
	if e.ClockOut != nil {
		clockOut = displayDateTime(ctx, *e.ClockOut)
	}

	duration := services.TimeEntryDuration(e.ClockIn, safeTime(e.ClockOut))

	userName := userNames[e.UserID]

	canEdit := isAdminOrDispatcher(currentUser) || (currentUser != nil && currentUser.ID == e.UserID)
	jobID := valueInt64(e.JobID)
	jobName := jobNames[jobID]
	if jobID > 0 && jobName == "" {
		jobName = fmt.Sprintf("Job #%d", jobID)
	}

	return templates.TimeEntryRow{
		ID:       e.ID,
		UserName: userName,
		IsManual: e.IsManual,
		ClockIn:  clockIn,
		ClockOut: clockOut,
		JobID:    jobID,
		JobName:  jobName,
		Duration: duration,
		Notes:    e.Notes,
		CanEdit:  canEdit,
	}
}

func jobAllowed(jobs []*ent.Job, jobID int64) bool {
	for _, j := range jobs {
		if j.ID == jobID {
			return true
		}
	}
	return false
}

func parseOptionalID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func valueInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func safeTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
