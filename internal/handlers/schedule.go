package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

type ScheduleHandler struct {
	jobSvc    *services.JobService
	custSvc   *services.CustomerService
	statusSvc *services.StatusService
}

func NewScheduleHandler(jobSvc *services.JobService, custSvc *services.CustomerService, statusSvc *services.StatusService) *ScheduleHandler {
	return &ScheduleHandler{jobSvc: jobSvc, custSvc: custSvc, statusSvc: statusSvc}
}

func (h *ScheduleHandler) Index(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	switch view {
	case "week":
		h.Week(w, r)
	case "day":
		h.Day(w, r)
	default:
		h.Month(w, r)
	}
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
		calJobs[i] = calendarJob(j, custMap, statuses)
	}

	weeks := buildMonthGrid(year, month, calJobs, loc)
	data := templates.SchedulePageData{
		Title:     time.Date(year, month, 1, 0, 0, 0, 0, loc).Format("January 2006"),
		Weeks:     weeks,
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
			cj := calendarJob(j, custMap, statuses)
			cj.Hour = j.StartTime.Hour()
			if j.StartTime == nil {
				continue
			}
			if j.StartTime.Year() == d.Year() && j.StartTime.YearDay() == d.YearDay() {
				day.Jobs = append(day.Jobs, cj)
			}
		}
		days = append(days, day)
	}

	prev := date.AddDate(0, 0, -7)
	next := date.AddDate(0, 0, 7)
	data := templates.SchedulePageData{
		Title:    fmt.Sprintf("%s — %s, %d", start.Format("Jan 2"), end.Format("Jan 2"), end.Year()),
		Days:     days,
		PrevDate: prev.Format("2006-01-02"),
		NextDate: next.Format("2006-01-02"),
		Date:     date.Format("2006-01-02"),
		IsWeek:   true,
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
		cj := calendarJob(j, custMap, statuses)
		cj.Hour = j.StartTime.Hour()
		calJobs = append(calJobs, cj)
	}

	prev := date.AddDate(0, 0, -1)
	next := date.AddDate(0, 0, 1)
	data := templates.SchedulePageData{
		Title:    date.Format("Monday, January 2, 2006"),
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

func calendarJob(j *ent.Job, custMap map[int64]string, statuses []*ent.Status) templates.CalendarJob {
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
		cj.Time = j.StartTime.Format("3:04 PM")
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
