package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

type ScheduleHandler struct {
	jobSvc     *services.JobService
	custSvc    *services.CustomerService
	statusSvc  *services.StatusService
	userSvc    *services.UserService
	locSvc     *services.LocationService
	invoiceSvc *services.InvoiceService
	cfg        *config.Config
}

func NewScheduleHandler(jobSvc *services.JobService, custSvc *services.CustomerService, statusSvc *services.StatusService, userSvc *services.UserService, locSvc *services.LocationService, invoiceSvc *services.InvoiceService, cfg *config.Config) *ScheduleHandler {
	return &ScheduleHandler{jobSvc: jobSvc, custSvc: custSvc, statusSvc: statusSvc, userSvc: userSvc, locSvc: locSvc, invoiceSvc: invoiceSvc, cfg: cfg}
}

func (h *ScheduleHandler) Index(w http.ResponseWriter, r *http.Request) {
	tab, period, explicit := h.resolveScheduleTabPeriod(r)
	if explicit {
		if u, _ := middleware.UserFromContext(r.Context()); u != nil && (tab != "dispatch" || isAdminOrDispatcher(u)) {
			if err := h.userSvc.UpdateSchedulePreferences(r.Context(), u.ID, tab, period); err != nil {
				slog.Error("update schedule preferences", "error", err, "user_id", u.ID)
			}
		}
	}
	r = requestWithScheduleTabPeriod(r, tab, period)
	switch tab {
	case "calendar":
		switch period {
		case "week":
			h.Week(w, r)
		case "day":
			h.Day(w, r)
		default:
			h.Month(w, r)
		}
	case "list":
		h.List(w, r)
	case "dispatch":
		h.Dispatch(w, r)
	case "map":
		h.Map(w, r)
	default:
		h.Month(w, r)
	}
}

func (h *ScheduleHandler) resolveScheduleTabPeriod(r *http.Request) (string, string, bool) {
	q := r.URL.Query()
	explicit := q.Has("tab") || q.Has("period")
	if explicit {
		tab, period := scheduleTabPeriod(r)
		return tab, period, true
	}

	if u, _ := middleware.UserFromContext(r.Context()); u != nil {
		tab, period := normalizeScheduleTabPeriod(u.LastScheduleTab, u.LastSchedulePeriod)
		if tab == "dispatch" && !isAdminOrDispatcher(u) {
			tab = "calendar"
			period = normalizeSchedulePeriod(period, tab)
		}
		return tab, period, false
	}

	return "calendar", "month", false
}

func requestWithScheduleTabPeriod(r *http.Request, tab, period string) *http.Request {
	clone := r.Clone(r.Context())
	u := *r.URL
	q := u.Query()
	q.Set("tab", tab)
	q.Set("period", period)
	u.RawQuery = q.Encode()
	clone.URL = &u
	return clone
}

func scheduleTabPeriod(r *http.Request) (string, string) {
	tab := r.URL.Query().Get("tab")
	period := r.URL.Query().Get("period")

	if tab == "" {
		switch r.URL.Query().Get("view") {
		case "list":
			tab = "list"
		case "calendar":
			tab = "calendar"
			if period == "" {
				period = "month"
			}
		case "week":
			tab = "calendar"
			period = "week"
		case "day":
			tab = "calendar"
			period = "day"
		case "dispatch":
			tab = "dispatch"
		case "map":
			tab = "map"
		}
	}

	return normalizeScheduleTabPeriod(tab, period)
}

func normalizeScheduleTabPeriod(tab, period string) (string, string) {
	if !services.ValidScheduleTab(tab) {
		tab = "calendar"
	}
	return tab, normalizeSchedulePeriod(period, tab)
}

func normalizeSchedulePeriod(period, tab string) string {
	if services.ValidSchedulePeriod(period) {
		return period
	}
	if tab == "dispatch" {
		return "day"
	}
	return "month"
}

func (h *ScheduleHandler) DispatchUpdate(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	date, err := time.ParseInLocation("2006-01-02", r.FormValue("date"), loc)
	if err != nil || jobID <= 0 || userID <= 0 {
		http.Error(w, "invalid schedule move", http.StatusBadRequest)
		return
	}
	j, err := h.jobSvc.GetByID(r.Context(), jobID)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	hour := 8
	minute := 0
	duration := 1
	if j.StartTime != nil && !j.StartTime.IsZero() {
		localStart := j.StartTime.In(loc)
		hour = localStart.Hour()
		minute = localStart.Minute()
	}
	if j.StartTime != nil && j.EndTime != nil && !j.EndTime.IsZero() {
		if d := int(j.EndTime.Sub(*j.StartTime).Hours()); d > 0 {
			duration = d
		}
	}
	if formHour := r.FormValue("hour"); formHour != "" {
		parsedHour, _ := strconv.Atoi(formHour)
		if parsedHour < 0 || parsedHour > 23 {
			http.Error(w, "invalid schedule move", http.StatusBadRequest)
			return
		}
		hour = parsedHour
		minute = 0
	}
	if formDuration := r.FormValue("duration"); formDuration != "" {
		parsedDuration, _ := strconv.Atoi(formDuration)
		if parsedDuration > 0 {
			duration = parsedDuration
		}
	}
	start := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
	end := start.Add(time.Duration(duration) * time.Hour)
	if _, err := h.jobSvc.Move(r.Context(), jobID, userID, start, end); err != nil {
		http.Error(w, "move job", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ScheduleHandler) CalendarMove(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	date, err := time.ParseInLocation("2006-01-02", r.FormValue("date"), loc)
	if err != nil || jobID <= 0 {
		http.Error(w, "invalid calendar move", http.StatusBadRequest)
		return
	}
	j, err := h.jobSvc.GetByID(r.Context(), jobID)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	hour := 8
	minute := 0
	duration := 1
	if j.StartTime != nil && !j.StartTime.IsZero() {
		hour = j.StartTime.In(loc).Hour()
		minute = j.StartTime.In(loc).Minute()
	}
	if formHour := r.FormValue("hour"); formHour != "" {
		parsedHour, _ := strconv.Atoi(formHour)
		if parsedHour < 0 || parsedHour > 23 {
			http.Error(w, "invalid calendar move", http.StatusBadRequest)
			return
		}
		hour = parsedHour
		minute = 0
	}
	if j.StartTime != nil && j.EndTime != nil && !j.EndTime.IsZero() {
		d := int(j.EndTime.Sub(*j.StartTime).Hours())
		if d > 0 {
			duration = d
		}
	}
	if formDuration := r.FormValue("duration"); formDuration != "" {
		parsedDuration, _ := strconv.Atoi(formDuration)
		if parsedDuration > 0 {
			duration = parsedDuration
		}
	}
	start := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
	end := start.Add(time.Duration(duration) * time.Hour)
	if _, err := h.jobSvc.MoveTime(r.Context(), jobID, start, end); err != nil {
		http.Error(w, "move job", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ScheduleHandler) Month(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	now := time.Now().In(loc)
	year, month := parseYearMonth(r, now)
	start, end := monthRange(year, month, loc)
	jobs, _ := h.jobsByDateRange(r, start, end)

	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	calJobs := make([]templates.CalendarJob, len(jobs))
	for i, j := range jobs {
		calJobs[i] = calendarJob(r.Context(), j, custMap, statuses)
	}

	weeks := buildMonthGrid(year, month, calJobs, loc)
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	data := templates.SchedulePageData{
		Title:     scheduleMonthYearTitle(monthStart),
		Tab:       "calendar",
		Period:    "month",
		Weeks:     weeks,
		Date:      monthStart.Format("2006-01-02"),
		PrevYear:  prevMonthYear(year, month),
		PrevMonth: prevMonthMonth(year, month),
		NextYear:  nextMonthYear(year, month),
		NextMonth: nextMonthMonth(year, month),
		IsMonth:   true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) Week(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	date := parseDateParam(r, "date", loc)
	start, end := weekRange(date, loc)

	jobs, _ := h.jobsByDateRange(r, start, end)
	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	var days []templates.ScheduleDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := templates.ScheduleDay{
			Date:    d.Format("2006-01-02"),
			DayName: d.Format("Mon"),
			DayNum:  d.Day(),
			IsToday: isToday(d, loc),
		}
		for _, j := range jobs {
			if j.StartTime == nil {
				continue
			}
			cj := calendarJob(r.Context(), j, custMap, statuses)
			cj.Hour = j.StartTime.Hour()
			if j.StartTime.Year() == d.Year() && j.StartTime.YearDay() == d.YearDay() {
				day.Jobs = append(day.Jobs, cj)
			}
		}
		days = append(days, day)
	}

	prev := date.AddDate(0, 0, -7)
	next := date.AddDate(0, 0, 7)
	data := templates.SchedulePageData{
		Title:    scheduleMonthYearTitle(start),
		Tab:      "calendar",
		Period:   "week",
		Days:     days,
		PrevDate: prev.Format("2006-01-02"),
		NextDate: next.Format("2006-01-02"),
		Date:     date.Format("2006-01-02"),
		IsWeek:   true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) List(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	period := parsePeriod(r, "month")
	date := parseDateParam(r, "date", loc)
	start, end := dispatchRange(period, date, loc)
	prev, next := dispatchPrevNext(period, date)

	jobs, _ := h.jobsByDateRange(r, start, end)
	jobsData := h.calendarJobs(r, jobs)
	var days []templates.ScheduleDay
	var weeks []templates.WeekData
	if period == "week" {
		days = h.listWeekDays(r, jobs, start, end, loc)
	} else if period == "day" {
		jobsData = h.listDayJobs(r, jobs, loc)
	} else {
		weeks = buildMonthGrid(start.Year(), start.Month(), jobsData, loc)
	}
	data := templates.SchedulePageData{
		Title:    scheduleTitle(r.Context(), period, date, start),
		Tab:      "list",
		Weeks:    weeks,
		Days:     days,
		Jobs:     jobsData,
		PrevDate: prev.Format("2006-01-02"),
		NextDate: next.Format("2006-01-02"),
		Date:     date.Format("2006-01-02"),
		Period:   period,
		IsList:   true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) Dispatch(w http.ResponseWriter, r *http.Request) {
	u, _ := middleware.UserFromContext(r.Context())
	if !isAdminOrDispatcher(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	loc := middleware.CompanyLocation(r.Context())
	date := parseDateParam(r, "date", loc)
	_, period := scheduleTabPeriod(r)
	start, end := dispatchRange(period, date, loc)
	jobs, _ := h.jobsByDateRange(r, start, end)
	unscheduled, _ := h.unscheduledJobs(r)
	techs := h.scheduleTechs(r)
	columns := []templates.DispatchColumn{}
	if period == "day" {
		columns = h.dispatchColumns(r, techs, jobs)
	}
	matrix := h.dispatchMatrix(r, period, start, end, techs, jobs)
	prev, next := dispatchPrevNext(period, date)
	data := templates.SchedulePageData{
		Title:           dispatchTitle(r.Context(), period, start, end),
		PrevDate:        prev.Format("2006-01-02"),
		NextDate:        next.Format("2006-01-02"),
		Date:            date.Format("2006-01-02"),
		UnscheduledJobs: h.calendarJobs(r, unscheduled),
		DispatchColumns: columns,
		DispatchMatrix:  matrix,
		Techs:           techs,
		Tab:             "dispatch",
		Period:          period,
		IsDispatch:      true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) Map(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	now := time.Now().In(loc)
	start, end := monthRange(now.Year(), now.Month(), loc)
	jobs, _ := h.jobsByDateRange(r, start, end)
	mapJobs, emptyMessage := h.mapJobs(r, jobs)
	data := templates.SchedulePageData{
		Title:           "Schedule Map",
		Tab:             "map",
		Period:          "month",
		Jobs:            h.calendarJobs(r, jobs),
		MapJobs:         mapJobs,
		MapEmptyMessage: emptyMessage,
		Date:            now.Format("2006-01-02"),
		TileURL:         h.mapTileURL(r),
		IsMap:           true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) Day(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	date := parseDateParam(r, "date", loc)
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1).Add(-time.Second)

	jobs, _ := h.jobsByDateRange(r, start, end)
	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	var calJobs []templates.CalendarJob
	for _, j := range jobs {
		cj := calendarJob(r.Context(), j, custMap, statuses)
		cj.Hour = j.StartTime.Hour()
		calJobs = append(calJobs, cj)
	}

	prev := date.AddDate(0, 0, -1)
	next := date.AddDate(0, 0, 1)
	data := templates.SchedulePageData{
		Title:    displayDate(r.Context(), date),
		Tab:      "calendar",
		Period:   "day",
		Jobs:     calJobs,
		PrevDate: prev.Format("2006-01-02"),
		NextDate: next.Format("2006-01-02"),
		Date:     date.Format("2006-01-02"),
		IsDay:    true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) jobsByDateRange(r *http.Request, start, end time.Time) ([]*ent.Job, error) {
	u, _ := middleware.UserFromContext(r.Context())
	if isAdminOrDispatcher(u) {
		return h.jobSvc.ListByDateRange(r.Context(), start, end)
	}
	if u == nil {
		return nil, nil
	}
	return h.jobSvc.ListAssignedByDateRange(r.Context(), u.ID, start, end)
}

func (h *ScheduleHandler) unscheduledJobs(r *http.Request) ([]*ent.Job, error) {
	u, _ := middleware.UserFromContext(r.Context())
	if isAdminOrDispatcher(u) {
		return h.jobSvc.ListUnscheduled(r.Context())
	}
	if u == nil {
		return nil, nil
	}
	return h.jobSvc.ListAssignedUnscheduled(r.Context(), u.ID)
}

func (h *ScheduleHandler) scheduleTechs(r *http.Request) []templates.ScheduleTech {
	users, _ := h.userSvc.ListAll(r.Context())
	techs := make([]templates.ScheduleTech, 0, len(users))
	for _, u := range users {
		if !u.IsActive {
			continue
		}
		techs = append(techs, templates.ScheduleTech{ID: u.ID, Name: u.Name})
	}
	return techs
}

func (h *ScheduleHandler) dispatchColumns(r *http.Request, techs []templates.ScheduleTech, jobs []*ent.Job) []templates.DispatchColumn {
	calJobs := h.calendarJobs(r, jobs)
	jobsByTech := make(map[int64][]templates.CalendarJob, len(techs))
	for i, job := range jobs {
		assignments, _ := h.jobSvc.Assignments(r.Context(), job.ID)
		if len(assignments) == 0 {
			continue
		}
		calJobs[i].TechID = assignments[0].UserID
		jobsByTech[assignments[0].UserID] = append(jobsByTech[assignments[0].UserID], calJobs[i])
	}
	columns := make([]templates.DispatchColumn, 0, len(techs))
	for _, tech := range techs {
		columns = append(columns, templates.DispatchColumn{Tech: tech, Jobs: jobsByTech[tech.ID]})
	}
	return columns
}

func (h *ScheduleHandler) dispatchMatrix(r *http.Request, period string, start, end time.Time, techs []templates.ScheduleTech, jobs []*ent.Job) templates.DispatchMatrix {
	columns := dispatchMatrixColumns(r.Context(), period, start, end, middleware.CompanyLocation(r.Context()))
	jobsByTechColumn := make(map[int64]map[string][]templates.CalendarJob, len(techs))
	calJobs := h.calendarJobs(r, jobs)

	for i, job := range jobs {
		if job.StartTime == nil {
			continue
		}
		assignments, _ := h.jobSvc.Assignments(r.Context(), job.ID)
		if len(assignments) == 0 {
			continue
		}
		techID := assignments[0].UserID
		columnKey := dispatchMatrixColumnKey(period, *job.StartTime)
		if columnKey == "" {
			continue
		}
		if jobsByTechColumn[techID] == nil {
			jobsByTechColumn[techID] = make(map[string][]templates.CalendarJob, len(columns))
		}
		calJobs[i].TechID = techID
		jobsByTechColumn[techID][columnKey] = append(jobsByTechColumn[techID][columnKey], calJobs[i])
	}

	rows := make([]templates.DispatchMatrixRow, 0, len(techs))
	for _, tech := range techs {
		cells := make([]templates.DispatchMatrixCell, 0, len(columns))
		for _, column := range columns {
			cells = append(cells, templates.DispatchMatrixCell{
				Column: column,
				Jobs:   jobsByTechColumn[tech.ID][column.Key],
			})
		}
		rows = append(rows, templates.DispatchMatrixRow{Tech: tech, Cells: cells})
	}

	return templates.DispatchMatrix{Period: period, Columns: columns, Rows: rows}
}

func dispatchMatrixColumns(ctx context.Context, period string, start, end time.Time, loc *time.Location) []templates.DispatchMatrixColumn {
	if period == "day" {
		columns := make([]templates.DispatchMatrixColumn, 0, 24)
		for hour := 0; hour <= 23; hour++ {
			columns = append(columns, templates.DispatchMatrixColumn{
				Key:   fmt.Sprintf("%02d", hour),
				Label: fmt.Sprintf("%02d:00", hour),
				Date:  start.Format("2006-01-02"),
				Hour:  hour,
			})
		}
		return columns
	}

	columns := []templates.DispatchMatrixColumn{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		columns = append(columns, templates.DispatchMatrixColumn{
			Key:     d.Format("2006-01-02"),
			Label:   displayDate(ctx, d),
			Date:    d.Format("2006-01-02"),
			IsToday: isToday(d, loc),
		})
	}
	return columns
}

func dispatchMatrixColumnKey(period string, start time.Time) string {
	if period == "day" {
		return fmt.Sprintf("%02d", start.Hour())
	}
	return start.Format("2006-01-02")
}

func (h *ScheduleHandler) customerMapForJobs(r *http.Request, jobs []*ent.Job) map[int64]string {
	ids := make([]int64, 0, len(jobs))
	seen := make(map[int64]struct{}, len(jobs))
	for _, j := range jobs {
		if j.CustomerID <= 0 {
			continue
		}
		if _, ok := seen[j.CustomerID]; ok {
			continue
		}
		seen[j.CustomerID] = struct{}{}
		ids = append(ids, j.CustomerID)
	}
	customers, _ := h.custSvc.ListByIDs(r.Context(), ids)
	return customerMap(customers)
}

func calendarJob(ctx context.Context, j *ent.Job, custMap map[int64]string, statuses []*ent.Status) templates.CalendarJob {
	cj := templates.CalendarJob{
		ID:      j.ID,
		JobType: j.JobType,
	}
	if j.CustomerID > 0 {
		cj.Customer = custMap[j.CustomerID]
	}
	cj.StatusName = statusName(statuses, j.StatusID)
	cj.StatusColor = statusColor(statuses, j.StatusID)
	if j.StartTime != nil && !j.StartTime.IsZero() {
		cj.Time = displayTime(ctx, *j.StartTime)
		cj.Date = displayDate(ctx, *j.StartTime)
		cj.DateISO = j.StartTime.Format("2006-01-02")
		cj.Day = j.StartTime.Day()
		cj.Hour = j.StartTime.Hour()

		// Calculate duration, cap at end of day
		if j.EndTime != nil && !j.EndTime.IsZero() {
			duration := j.EndTime.Hour() - j.StartTime.Hour()
			if duration < 1 {
				duration = 1
			}
			maxDuration := 24 - cj.Hour
			if duration > maxDuration {
				duration = maxDuration
			}
			cj.Duration = duration
		} else {
			cj.Duration = 1
		}
	}
	return cj
}

func parsePeriod(r *http.Request, fallback string) string {
	switch r.URL.Query().Get("period") {
	case "month", "week", "day":
		return r.URL.Query().Get("period")
	default:
		return fallback
	}
}

func (h *ScheduleHandler) calendarJobs(r *http.Request, jobs []*ent.Job) []templates.CalendarJob {
	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	calJobs := make([]templates.CalendarJob, 0, len(jobs))
	for _, j := range jobs {
		calJobs = append(calJobs, calendarJob(r.Context(), j, custMap, statuses))
	}
	return calJobs
}

func (h *ScheduleHandler) listWeekDays(r *http.Request, jobs []*ent.Job, start, end time.Time, loc *time.Location) []templates.ScheduleDay {
	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	locations := h.locationMapForJobs(r, jobs)

	days := make([]templates.ScheduleDay, 0, 7)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := templates.ScheduleDay{
			Date:    d.Format("2006-01-02"),
			DayName: d.Format("Mon"),
			DayNum:  d.Day(),
			IsToday: isToday(d, loc),
		}

		dayJobs := make([]*ent.Job, 0)
		for _, j := range jobs {
			if j.StartTime == nil || j.StartTime.IsZero() {
				continue
			}
			jobStart := j.StartTime.In(loc)
			if jobStart.Year() == d.Year() && jobStart.YearDay() == d.YearDay() {
				dayJobs = append(dayJobs, j)
			}
		}
		sort.SliceStable(dayJobs, func(i, j int) bool {
			return dayJobs[i].StartTime.In(loc).Before(dayJobs[j].StartTime.In(loc))
		})
		for _, j := range dayJobs {
			day.Jobs = append(day.Jobs, listWeekCalendarJob(r.Context(), j, custMap, statuses, locations, loc))
		}
		days = append(days, day)
	}
	return days
}

func (h *ScheduleHandler) locationMapForJobs(r *http.Request, jobs []*ent.Job) map[int64]*ent.Location {
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, j := range jobs {
		if j.LocationID == nil || *j.LocationID <= 0 {
			continue
		}
		if _, ok := seen[*j.LocationID]; ok {
			continue
		}
		seen[*j.LocationID] = struct{}{}
		ids = append(ids, *j.LocationID)
	}
	locations, _ := h.locSvc.ListByIDs(r.Context(), ids)
	byID := make(map[int64]*ent.Location, len(locations))
	for _, l := range locations {
		byID[l.ID] = l
	}
	return byID
}

func listWeekCalendarJob(ctx context.Context, j *ent.Job, custMap map[int64]string, statuses []*ent.Status, locations map[int64]*ent.Location, loc *time.Location) templates.CalendarJob {
	cj := calendarJob(ctx, j, custMap, statuses)
	cj.TimeRange = scheduleJobTimeRange(ctx, j)
	cj.StartTime = scheduleJobStartTime(ctx, j)
	cj.EndTime = scheduleJobEndTime(ctx, j)
	if j.LocationID != nil {
		if l := locations[*j.LocationID]; l != nil {
			cj.Address = services.LocationAddress(l)
		}
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		cj.Hour = j.StartTime.In(loc).Hour()
	}
	return cj
}

func (h *ScheduleHandler) listDayJobs(r *http.Request, jobs []*ent.Job, loc *time.Location) []templates.CalendarJob {
	custMap := h.customerMapForJobs(r, jobs)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	invoiceStatuses, _ := h.statusSvc.ByObjectType(r.Context(), "invoice")
	locations := h.locationMapForJobs(r, jobs)
	invoices := h.latestInvoiceMapForJobs(r, jobs)

	dayJobs := make([]*ent.Job, 0, len(jobs))
	for _, j := range jobs {
		if j.StartTime == nil || j.StartTime.IsZero() {
			continue
		}
		dayJobs = append(dayJobs, j)
	}
	sort.SliceStable(dayJobs, func(i, j int) bool {
		return dayJobs[i].StartTime.In(loc).Before(dayJobs[j].StartTime.In(loc))
	})

	calJobs := make([]templates.CalendarJob, 0, len(dayJobs))
	for _, j := range dayJobs {
		cj := listWeekCalendarJob(r.Context(), j, custMap, statuses, locations, loc)
		if assignments, _ := h.jobSvc.Assignments(r.Context(), j.ID); len(assignments) > 0 {
			cj.Assignee = assignments[0].Name
			cj.AssigneeInitials = initials(assignments[0].Name)
			cj.AssigneeColor = assigneeColor(assignments[0].Name)
		}
		if inv := invoices[j.ID]; inv != nil {
			cj.InvoiceID = inv.ID
			cj.InvoiceStatusName = statusName(invoiceStatuses, inv.StatusID)
			cj.InvoiceStatusColor = statusColor(invoiceStatuses, inv.StatusID)
		}
		calJobs = append(calJobs, cj)
	}
	return calJobs
}

func (h *ScheduleHandler) latestInvoiceMapForJobs(r *http.Request, jobs []*ent.Job) map[int64]*ent.Invoice {
	jobIDs := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		jobIDs = append(jobIDs, j.ID)
	}
	invoices, _ := h.invoiceSvc.LatestByJobIDs(r.Context(), jobIDs)
	return invoices
}

func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return strings.ToUpper(firstRune(parts[0]))
	}
	return strings.ToUpper(firstRune(parts[0]) + firstRune(parts[len(parts)-1]))
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

func assigneeColor(name string) string {
	palette := []string{"#2563EB", "#7C3AED", "#0F766E", "#B45309", "#BE123C", "#4338CA", "#047857"}
	if name == "" {
		return palette[0]
	}
	hash := 0
	for _, r := range name {
		hash += int(r)
	}
	return palette[hash%len(palette)]
}

func scheduleJobTimeRange(ctx context.Context, j *ent.Job) string {
	if j.StartTime == nil || j.StartTime.IsZero() {
		return ""
	}
	start := displayTime(ctx, *j.StartTime)
	if j.EndTime == nil || j.EndTime.IsZero() {
		return start
	}
	return fmt.Sprintf("%s-%s", start, displayTime(ctx, *j.EndTime))
}

func scheduleJobStartTime(ctx context.Context, j *ent.Job) string {
	if j.StartTime == nil || j.StartTime.IsZero() {
		return ""
	}
	return displayTime(ctx, *j.StartTime)
}

func scheduleJobEndTime(ctx context.Context, j *ent.Job) string {
	if j.EndTime == nil || j.EndTime.IsZero() {
		return ""
	}
	return displayTime(ctx, *j.EndTime)
}

func (h *ScheduleHandler) mapJobs(r *http.Request, jobs []*ent.Job) ([]templates.CalendarJob, string) {
	if len(jobs) == 0 {
		return nil, "No scheduled jobs are in the current month."
	}
	locationIDs := make([]int64, 0, len(jobs))
	seen := make(map[int64]struct{})
	jobsWithLocation := 0
	for _, j := range jobs {
		if j.LocationID == nil || *j.LocationID <= 0 {
			continue
		}
		jobsWithLocation++
		if _, ok := seen[*j.LocationID]; ok {
			continue
		}
		seen[*j.LocationID] = struct{}{}
		locationIDs = append(locationIDs, *j.LocationID)
	}
	if jobsWithLocation == 0 {
		return nil, "Scheduled jobs in this month do not have linked locations yet. Add a job location to show them on the map."
	}
	locations, _ := h.locSvc.ListByIDs(r.Context(), locationIDs)
	locByID := make(map[int64]*ent.Location, len(locations))
	geocodeAttempted := false
	geocoderURL := h.geocoderURL(r)
	geocodeFailed := false
	needsGeocoding := false
	for _, l := range locations {
		if geocoderURL != "" && !geocodeAttempted && (l.Latitude == nil || l.Longitude == nil) {
			geocodeAttempted = true
			if geocoded, err := h.locSvc.Geocode(r.Context(), l, geocoderURL); err == nil {
				l = geocoded
				if l.Latitude == nil || l.Longitude == nil {
					slog.Info("schedule map geocode returned no result", "location_id", l.ID, "geocoder_url", geocoderURL)
				}
			} else {
				geocodeFailed = true
				slog.Warn("schedule map geocode failed", "location_id", l.ID, "geocoder_url", geocoderURL, "error", err)
			}
		}
		if l.Latitude == nil || l.Longitude == nil {
			needsGeocoding = true
		}
		locByID[l.ID] = l
	}
	calJobs := h.calendarJobs(r, jobs)
	mapJobs := make([]templates.CalendarJob, 0, len(calJobs))
	for i, j := range jobs {
		if j.LocationID == nil {
			continue
		}
		l := locByID[*j.LocationID]
		if l == nil || l.Latitude == nil || l.Longitude == nil {
			continue
		}
		calJobs[i].Lat = *l.Latitude
		calJobs[i].Lng = *l.Longitude
		mapJobs = append(mapJobs, calJobs[i])
	}
	if len(mapJobs) > 0 {
		return mapJobs, ""
	}
	if geocodeFailed {
		return nil, "Scheduled job locations could not be geocoded. Check the configured geocoder URL and server logs."
	}
	if geocoderURL == "" && needsGeocoding {
		return nil, "Scheduled job locations need coordinates. Configure a geocoder URL in Settings > Map, or enter coordinates manually."
	}
	if geocoderURL != "" && needsGeocoding {
		return nil, "Scheduled job locations are waiting for geocoding. Refresh the map to geocode the next location."
	}
	return nil, "No geocoded scheduled jobs are available yet."
}

func (h *ScheduleHandler) mapTileURL(r *http.Request) string {
	if cs := middleware.CompanyFromContext(r.Context()); cs != nil && cs.MapTileURL != "" {
		return cs.MapTileURL
	}
	return h.cfg.TileURL
}

func (h *ScheduleHandler) geocoderURL(r *http.Request) string {
	if cs := middleware.CompanyFromContext(r.Context()); cs != nil && cs.GeocoderURL != "" {
		return cs.GeocoderURL
	}
	return h.cfg.GeocoderURL
}

func parseDateParam(r *http.Request, key string, loc *time.Location) time.Time {
	ds := r.URL.Query().Get(key)
	if ds == "" {
		return time.Now().In(loc)
	}
	t, err := time.ParseInLocation("2006-01-02", ds, loc)
	if err != nil {
		return time.Now().In(loc)
	}
	return t
}

func weekRange(date time.Time, loc *time.Location) (time.Time, time.Time) {
	weekday := int(date.Weekday())
	start := date.AddDate(0, 0, -weekday)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 7).Add(-time.Second)
	return start, end
}

func dispatchRange(period string, date time.Time, loc *time.Location) (time.Time, time.Time) {
	switch period {
	case "month":
		return monthRange(date.Year(), date.Month(), loc)
	case "week":
		return weekRange(date, loc)
	default:
		start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 0, 1).Add(-time.Second)
	}
}

func dispatchPrevNext(period string, date time.Time) (time.Time, time.Time) {
	switch period {
	case "month":
		return date.AddDate(0, -1, 0), date.AddDate(0, 1, 0)
	case "week":
		return date.AddDate(0, 0, -7), date.AddDate(0, 0, 7)
	default:
		return date.AddDate(0, 0, -1), date.AddDate(0, 0, 1)
	}
}

func dispatchTitle(ctx context.Context, period string, start, end time.Time) string {
	switch period {
	case "month":
		return scheduleMonthYearTitle(start)
	case "week":
		return scheduleMonthYearTitle(start)
	default:
		return displayDate(ctx, start)
	}
}

func scheduleTitle(ctx context.Context, period string, date, start time.Time) string {
	switch period {
	case "month", "week":
		return scheduleMonthYearTitle(start)
	default:
		return displayDate(ctx, date)
	}
}

func scheduleMonthYearTitle(t time.Time) string {
	return t.Format("Jan, 2006")
}

func parseYearMonth(r *http.Request, now time.Time) (int, time.Month) {
	ys := r.URL.Query().Get("year")
	ms := r.URL.Query().Get("month")
	if ys == "" || ms == "" {
		return now.Year(), now.Month()
	}
	y, _ := strconv.Atoi(ys)
	m, _ := strconv.Atoi(ms)
	if y > 0 && m >= 1 && m <= 12 {
		return y, time.Month(m)
	}
	return now.Year(), now.Month()
}

func monthRange(year int, month time.Month, loc *time.Location) (time.Time, time.Time) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return start, end
}

func prevMonthYear(year int, month time.Month) int {
	if month == 1 {
		return year - 1
	}
	return year
}

func prevMonthMonth(year int, month time.Month) int {
	if month == 1 {
		return 12
	}
	return int(month) - 1
}

func nextMonthYear(year int, month time.Month) int {
	if month == 12 {
		return year + 1
	}
	return year
}

func nextMonthMonth(year int, month time.Month) int {
	if month == 12 {
		return 1
	}
	return int(month) + 1
}

func buildMonthGrid(year int, month time.Month, jobs []templates.CalendarJob, loc *time.Location) []templates.WeekData {
	first := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	startDay := int(first.Weekday())
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()

	jobsByDay := make(map[int][]templates.CalendarJob)
	for _, j := range jobs {
		jobsByDay[j.Day] = append(jobsByDay[j.Day], j)
	}

	var weeks []templates.WeekData
	day := 1
	for w := 0; w < 6 && day <= daysInMonth; w++ {
		var days []templates.DayData
		for d := 0; d < 7; d++ {
			if (w == 0 && d < startDay) || day > daysInMonth {
				days = append(days, templates.DayData{DayNum: 0, IsToday: false})
			} else {
				date := time.Date(year, month, day, 0, 0, 0, 0, loc)
				days = append(days, templates.DayData{
					DayNum:  day,
					IsToday: isToday(date, loc),
					Date:    date.Format("2006-01-02"),
					Jobs:    jobsByDay[day],
				})
				day++
			}
		}
		weeks = append(weeks, templates.WeekData{Days: days})
	}
	return weeks
}

func isToday(d time.Time, loc *time.Location) bool {
	n := time.Now().In(loc)
	return d.Year() == n.Year() && d.Month() == n.Month() && d.Day() == n.Day()
}
