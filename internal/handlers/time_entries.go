package handlers

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
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
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, _ := authorization.NavigationPrincipal(r.Context())
	canViewAll := p.Allows("directory.view", authorization.All) && (p.Allows("time.view", authorization.All) || p.Allows("time.view", authorization.Assigned))
	filterUserID, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if !p.Allows("time.view", authorization.All) && !p.Allows("time.view", authorization.Assigned) {
		filterUserID = user.ID
	}

	search := r.URL.Query().Get("search")
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")
	from, before, err := parseTimeEntryDateRange(dateFrom, dateTo, middleware.CompanyLocation(r.Context()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	entries, total, err := h.svc.List(r.Context(), services.TimeEntryListFilter{
		UserID:        filterUserID,
		Search:        search,
		ClockInFrom:   from,
		ClockInBefore: before,
	}, page, perPage)
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
		UserID:         filterUserID,
		DateFrom:       dateFrom,
		DateTo:         dateTo,
		ShowUserFilter: canViewAll,
		Users:          users,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		render(w, r, templates.TimeEntriesListContent(data))
		return
	}
	render(w, r, templates.TimeEntriesIndex(data))
}

func parseTimeEntryDateRange(dateFrom, dateTo string, loc *time.Location) (*time.Time, *time.Time, error) {
	var from, before *time.Time
	if dateFrom != "" {
		parsed, err := time.ParseInLocation("2006-01-02", dateFrom, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid date_from")
		}
		from = &parsed
	}
	if dateTo != "" {
		parsed, err := time.ParseInLocation("2006-01-02", dateTo, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid date_to")
		}
		exclusive := parsed.AddDate(0, 0, 1)
		before = &exclusive
	}
	if from != nil && before != nil && !from.Before(*before) {
		return nil, nil, fmt.Errorf("date_from must not be after date_to")
	}
	return from, before, nil
}

func (h *TimeEntryHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	view, ok := timeEntryAuthority(w, r, id, "")
	if !ok {
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
	userName := view.SubjectName
	jobID, jobName := h.timeEntryJob(r.Context(), entry)

	clockInStr := displayDateTime(r.Context(), entry.ClockIn)
	clockOutStr := ""
	if entry.ClockOut != nil {
		clockOutStr = displayDateTime(r.Context(), *entry.ClockOut)
	}
	duration := services.TimeEntryDuration(entry.ClockIn, safeTime(entry.ClockOut))

	gpsLat := ""
	gpsLon := ""
	if view.CanGPS && entry.Latitude != nil {
		gpsLat = fmt.Sprintf("%.6f", *entry.Latitude)
	}
	if view.CanGPS && entry.Longitude != nil {
		gpsLon = fmt.Sprintf("%.6f", *entry.Longitude)
	}

	data := templates.TimeEntryShowPageData{
		CanEdit:   view.CanCorrect,
		CanDelete: false,
		ID:        entry.ID,
		UserName:  userName,
		JobID:     jobID,
		JobName:   jobName,
		ClockIn:   clockInStr,
		ClockOut:  clockOutStr,
		Duration:  duration,
		IsManual:  entry.IsManual,
		Notes:     entry.Notes,
		GPSLat:    gpsLat,
		GPSLon:    gpsLon,
	}
	// Deletion is not Void: never advertise the retired erasure action.
	data.CanDelete = false
	if view.CanGPS {
		req, _ := authorization.FromContext(r.Context())
		facts, err := req.Service.ClockGPS(r.Context(), req.Actor, id)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		for _, fact := range facts {
			text := fmt.Sprintf("%.7f, %.7f; captured %s", fact.Latitude, fact.Longitude, displayDateTime(r.Context(), fact.CapturedAt))
			if fact.Accuracy != nil {
				text += fmt.Sprintf("; accuracy %g m", *fact.Accuracy)
			}
			if fact.Event == "in" {
				data.GPSIn = text
			} else {
				data.GPSOut = text
			}
		}
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

	in, ok := clockForm(w, r)
	if !ok {
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	_, err := req.Service.Clock(r.Context(), req.Actor, false, in)
	if err != nil {
		if errors.Is(err, services.ErrActiveTimeEntry) {
			http.Redirect(w, r, "/?flash=You+are+already+clocked+in", http.StatusSeeOther)
			return
		}
		clockError(w, r, err)
		return
	}
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

	entryID, err := strconv.ParseInt(r.FormValue("entry_id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.Error(w, "An exact entry_id is required", http.StatusBadRequest)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	in, ok := clockForm(w, r)
	if !ok {
		return
	}
	in.EntryID = entryID
	_, err = req.Service.Clock(r.Context(), req.Actor, true, in)
	if err != nil {
		clockError(w, r, err)
		return
	}
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
	view, ok := timeEntryAuthority(w, r, id, "time.correct")
	if !ok {
		return
	}

	entry, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
			clockOutStr = entry.ClockOut.In(loc).Format(time.RFC3339Nano)
		}

		data := templates.TimeEntryFormPageData{
			PreserveJob: view.JobID > 0 && !jobAllowed(jobs, view.JobID),
			Entry: &templates.TimeEntryFormEntry{
				ID:       entry.ID,
				JobID:    valueInt64(entry.JobID),
				ClockIn:  entry.ClockIn.In(loc).Format(time.RFC3339Nano),
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

	if r.Form.Has("latitude") || r.Form.Has("longitude") || r.Form.Has("captured_at") || r.Form.Has("accuracy") || r.Form.Has("gps") {
		http.Error(w, "GPS is immutable clock-only data", 400)
		return
	}
	params := services.TimeEntryUpdateParams{}
	loc := middleware.CompanyLocation(r.Context())
	if clockIn := r.FormValue("clock_in"); clockIn != "" {
		t, err := parseTimeEntryEdit(clockIn, loc)
		if err != nil {
			http.Error(w, "Invalid or ambiguous clock-in timestamp; include UTC offset", 400)
			return
		}
		if !t.Equal(entry.ClockIn) {
			params.ClockIn = &t
		}
	}
	if clockOut := r.FormValue("clock_out"); clockOut != "" {
		t, err := parseTimeEntryEdit(clockOut, loc)
		if err != nil {
			http.Error(w, "Invalid or ambiguous clock-out timestamp; include UTC offset", 400)
			return
		}
		if entry.ClockOut == nil || !t.Equal(*entry.ClockOut) {
			params.ClockOut = &t
		}
	}
	params.Notes = formPtr(r.FormValue("notes"))
	if r.Form.Has("job_id") {
		jobID, err := parseOptionalID(r.FormValue("job_id"))
		if err != nil || jobID < 0 {
			http.Error(w, "invalid job", 400)
			return
		}
		if jobID != view.JobID {
			if jobID > 0 {
				params.JobID = &jobID
			} else {
				params.ClearJob = true
			}
		}
	}
	req, _ := authorization.FromContext(r.Context())
	if err := req.Service.CorrectTimeEntry(r.Context(), req.Actor, id, authorization.TimeEntryCorrection{ClockIn: params.ClockIn, ClockOut: params.ClockOut, Notes: params.Notes, JobID: params.JobID, ClearJob: params.ClearJob}); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/time-entries?flash=Time+entry+updated", http.StatusSeeOther)
}

func (h *TimeEntryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	// Deletion cannot stand in for a reasoned terminal Void. Retain facts.
	authorizationError(w, r, authorization.ErrForbidden)
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
	if req, ok := authorization.FromContext(ctx); ok {
		if req.Service.ParentAccess(ctx, req.Actor, "job", *e.JobID, "") != nil {
			return 0, ""
		}
	}
	if j, err := h.jobSvc.GetByID(ctx, *e.JobID); err == nil && j != nil {
		return *e.JobID, jobDisplayName(j)
	}
	return *e.JobID, fmt.Sprintf("Job #%d", *e.JobID)
}

func (h *TimeEntryHandler) allowedJobsForUser(ctx context.Context, user *middleware.UserInfo) ([]*ent.Job, error) {
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return nil, authorization.ErrForbidden
	}
	p, err := req.Service.Principal(ctx, req.Actor)
	if err != nil {
		return nil, err
	}
	return h.jobSvc.ListAll(authorization.WithNavigationReads(ctx, p))
}

func timeEntryToRow(ctx context.Context, e *ent.TimeEntry, userNames map[int64]string, jobNames map[int64]string, currentUser *middleware.UserInfo) templates.TimeEntryRow {
	clockIn := displayDateTime(ctx, e.ClockIn)
	clockOut := ""
	if e.ClockOut != nil {
		clockOut = displayDateTime(ctx, *e.ClockOut)
	}

	duration := services.TimeEntryDuration(e.ClockIn, safeTime(e.ClockOut))

	userName := userNames[e.UserID]

	canEdit, canDelete := false, false
	jobID := valueInt64(e.JobID)
	jobName := jobNames[jobID]
	if jobID > 0 && jobName == "" {
		jobName = fmt.Sprintf("Job #%d", jobID)
	}
	if _, current := authorization.NavigationPrincipal(ctx); current {
		if req, ok := authorization.FromContext(ctx); ok {
			if view, err := req.Service.TimeEntryAccess(ctx, req.Actor, e.ID, ""); err == nil {
				canEdit = view.CanCorrect
				canDelete = false // reasoned Void replaces destructive deletion (#59)
				userName = view.SubjectName
			}
		}
		if jobNames[jobID] == "" {
			jobID, jobName = 0, ""
		}
	}

	return templates.TimeEntryRow{
		ID:        e.ID,
		UserName:  userName,
		IsManual:  e.IsManual,
		ClockIn:   clockIn,
		ClockOut:  clockOut,
		JobID:     jobID,
		JobName:   jobName,
		Duration:  duration,
		Notes:     e.Notes,
		CanEdit:   canEdit,
		CanDelete: canDelete,
	}
}

func timeEntryAuthority(w http.ResponseWriter, r *http.Request, id int64, permission string) (authorization.TimeEntryView, bool) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return authorization.TimeEntryView{}, false
	}
	view, err := req.Service.TimeEntryAccess(r.Context(), req.Actor, id, permission)
	if err != nil {
		authorizationError(w, r, err)
		return authorization.TimeEntryView{}, false
	}
	return view, true
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

func clockError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *authorization.ClockConflict
	if errors.As(err, &conflict) {
		status := http.StatusConflict
		if conflict.Code == "invalid_gps" || conflict.Code == "invalid_occurrence" {
			status = http.StatusBadRequest
		}
		http.Error(w, conflict.Code+": review this clock; submit explicitly without location if GPS is invalid", status)
		return
	}
	authorizationError(w, r, err)
}

// Explicit offsets preserve exact instants, including a daylight-saving fold.
// Legacy offset-free submissions are accepted only for a unique local instant.
func parseTimeEntryEdit(value string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	civil, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		civil, err = time.Parse("2006-01-02T15:04", value)
	}
	if err != nil {
		return time.Time{}, err
	}
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, offset := civil.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[offset] = true
	}
	matches := []time.Time{}
	for offset := range offsets {
		candidate := civil.Add(-time.Duration(offset) * time.Second)
		if candidate.In(loc).Format("2006-01-02T15:04:05.999999999") == civil.Format("2006-01-02T15:04:05.999999999") {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return time.Time{}, fmt.Errorf("ambiguous or nonexistent local time")
	}
	return matches[0], nil
}

func clockForm(w http.ResponseWriter, r *http.Request) (authorization.ClockCommand, bool) {
	in := authorization.ClockCommand{}
	if r.Header.Get("Content-Type") != "" || r.ContentLength != 0 {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			http.Error(w, "Clock forms require application/x-www-form-urlencoded", http.StatusUnsupportedMediaType)
			return in, false
		}
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid clock", 400)
		return in, false
	}
	if r.Form.Has("gps") {
		http.Error(w, "invalid_gps: use the clock location fields or explicitly submit without location", 400)
		return in, false
	}
	if r.Form.Has("occurred_at") || r.Form.Has("lease_id") {
		http.Error(w, "Web clocks use server time", 400)
		return in, false
	}
	if r.Form.Has("job_id") {
		id, err := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid_job", 400)
			return in, false
		}
		in.JobID = id
	}
	if r.Form.Has("latitude") || r.Form.Has("longitude") || r.Form.Has("captured_at") || r.Form.Has("accuracy") {
		g := &authorization.ClockGPS{}
		lat, e1 := strconv.ParseFloat(r.FormValue("latitude"), 64)
		lon, e2 := strconv.ParseFloat(r.FormValue("longitude"), 64)
		captured, e3 := time.Parse(time.RFC3339Nano, r.FormValue("captured_at"))
		if e1 != nil || e2 != nil || e3 != nil {
			http.Error(w, "invalid_gps: submit explicitly without location", 400)
			return in, false
		}
		g.Latitude = &lat
		g.Longitude = &lon
		g.CapturedAt = captured
		if r.Form.Has("accuracy") {
			accuracy, err := strconv.ParseFloat(r.FormValue("accuracy"), 64)
			if err != nil {
				http.Error(w, "invalid_gps", 400)
				return in, false
			}
			g.Accuracy = &accuracy
		}
		in.GPS = g
	}
	return in, true
}
