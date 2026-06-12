package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
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
	now := time.Now()
	year, month := parseYearMonth(r, now)
	start, end := monthRange(year, month)
	jobs, _ := h.jobSvc.ListByDateRange(r.Context(), start, end)

	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	statusNameMap := statusNameMapFunc(statuses)

	calJobs := make([]templates.CalendarJob, len(jobs))
	for i, j := range jobs {
		calJobs[i] = calendarJob(j, custMap, statusNameMap)
	}

	weeks := buildMonthGrid(year, month, calJobs)
	data := templates.SchedulePageData{
		Title:     time.Date(year, month, 1, 0, 0, 0, 0, time.Local).Format("January 2006"),
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
	date := parseDateParam(r, "date")
	start, end := weekRange(date)

	jobs, _ := h.jobSvc.ListByDateRange(r.Context(), start, end)
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	statusNameMap := statusNameMapFunc(statuses)

	var days []templates.ScheduleDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := templates.ScheduleDay{
			Date:    d.Format("2006-01-02"),
			DayName: d.Format("Mon"),
			DayNum:  d.Day(),
			IsToday: isToday(d),
		}
		for _, j := range jobs {
			cj := calendarJob(j, custMap, statusNameMap)
			cj.Hour = j.StartTime.Hour()
			if j.StartTime == nil { continue }
			if j.StartTime.Year() == d.Year() && j.StartTime.YearDay() == d.YearDay() {
				day.Jobs = append(day.Jobs, cj)
			}
		}
		days = append(days, day)
	}

	prev := date.AddDate(0, 0, -7)
	next := date.AddDate(0, 0, 7)
	data := templates.SchedulePageData{
		Title:   fmt.Sprintf("%s %d — %s %d, %d", start.Format("Jan 2"), start.Day(), end.Format("Jan 2"), end.Day(), end.Year()),
		Days:    days,
		PrevDate: prev.Format("2006-01-02"),
		NextDate: next.Format("2006-01-02"),
		Date:     date.Format("2006-01-02"),
		IsWeek:   true,
	}
	templates.SchedulePage(data).Render(r.Context(), w)
}

func (h *ScheduleHandler) Day(w http.ResponseWriter, r *http.Request) {
	date := parseDateParam(r, "date")
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1).Add(-time.Second)

	jobs, _ := h.jobSvc.ListByDateRange(r.Context(), start, end)
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "job")
	statusNameMap := statusNameMapFunc(statuses)

	var calJobs []templates.CalendarJob
	for _, j := range jobs {
		cj := calendarJob(j, custMap, statusNameMap)
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

func calendarJob(j *ent.Job, custMap map[int64]string, statusMap map[int64]string) templates.CalendarJob {
	cj := templates.CalendarJob{
		ID:      j.ID,
		JobType: j.JobType,
	}
	if j.CustomerID > 0 {
		cj.Customer = custMap[j.CustomerID]
	}
	if sID := statusID(j); sID > 0 {
		cj.StatusName = statusMap[sID]
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		cj.Time = j.StartTime.Format("3:04 PM")
		cj.Day = j.StartTime.Day()
	}
	return cj
}

func parseDateParam(r *http.Request, key string) time.Time {
	ds := r.URL.Query().Get(key)
	if ds == "" {
		return time.Now()
	}
	t, err := time.Parse("2006-01-02", ds)
	if err != nil {
		return time.Now()
	}
	return t
}

func weekRange(date time.Time) (time.Time, time.Time) {
	weekday := int(date.Weekday())
	start := date.AddDate(0, 0, -weekday)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 7).Add(-time.Second)
	return start, end
}

func statusNameMapFunc(statuses []*ent.Status) map[int64]string {
	m := make(map[int64]string, len(statuses))
	for _, s := range statuses {
		m[s.ID] = s.Name
	}
	return m
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

func monthRange(year int, month time.Month) (time.Time, time.Time) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return start, end
}

func prevMonthYear(year int, month time.Month) int {
	if month == 1 { return year - 1 }
	return year
}

func prevMonthMonth(year int, month time.Month) int {
	if month == 1 { return 12 }
	return int(month) - 1
}

func nextMonthYear(year int, month time.Month) int {
	if month == 12 { return year + 1 }
	return year
}

func nextMonthMonth(year int, month time.Month) int {
	if month == 12 { return 1 }
	return int(month) + 1
}

func buildMonthGrid(year int, month time.Month, jobs []templates.CalendarJob) []templates.WeekData {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	startDay := int(first.Weekday())
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local).Day()

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
				date := time.Date(year, month, day, 0, 0, 0, 0, time.Local)
				days = append(days, templates.DayData{
					DayNum:  day,
					IsToday: isToday(date),
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

func isToday(d time.Time) bool {
	n := time.Now()
	return d.Year() == n.Year() && d.Month() == n.Month() && d.Day() == n.Day()
}
