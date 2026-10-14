package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

type ScheduleHandler struct {
	userSvc *services.UserService
	cfg     *config.Config
}

func NewScheduleHandler(userSvc *services.UserService, cfg *config.Config) *ScheduleHandler {
	return &ScheduleHandler{userSvc: userSvc, cfg: cfg}
}

func (h *ScheduleHandler) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	savePreference := (query.Has("tab") || query.Has("period") || query.Has("view")) && query.Get("responsive") != "1"
	r, phoneDefault := restoreScheduleState(r)
	tab, period, _ := h.resolveScheduleTabPeriod(r)
	r = requestWithScheduleTabPeriod(r, tab, period)
	h.showSchedule(w, r, tab, period, phoneDefault, savePreference)
}

func (h *ScheduleHandler) resolveScheduleTabPeriod(r *http.Request) (string, string, bool) {
	q := r.URL.Query()
	if q.Has("tab") || q.Has("period") || q.Has("view") {
		tab, period := scheduleTabPeriod(r)
		return tab, period, true
	}
	if u, _ := middleware.UserFromContext(r.Context()); u != nil {
		tab, period := normalizeScheduleTabPeriod(u.LastScheduleTab, u.LastSchedulePeriod)
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
	tab, period := r.URL.Query().Get("tab"), r.URL.Query().Get("period")
	if tab == "" {
		switch r.URL.Query().Get("view") {
		case "list", "calendar", "dispatch", "map":
			tab = r.URL.Query().Get("view")
		case "week", "day":
			tab = "calendar"
			period = r.URL.Query().Get("view")
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
	if tab == "dispatch" || tab == "map" {
		return "day"
	}
	return "month"
}

func (h *ScheduleHandler) mapTileURL(r *http.Request) string {
	if cs := middleware.CompanyFromContext(r.Context()); cs != nil && cs.MapTileURL != "" {
		return safeMapTileURL(cs.MapTileURL)
	}
	if h.cfg == nil {
		return ""
	}
	return safeMapTileURL(h.cfg.TileURL)
}

func parseDateParam(r *http.Request, key string, loc *time.Location) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get(key), loc); err == nil {
		return t
	}
	return time.Now().In(loc)
}

// All ranges are half-open company-local day boundaries. AddDate preserves
// actual DST-short/long days instead of assuming 24 elapsed hours.
func dispatchRange(period string, date time.Time, loc *time.Location) (time.Time, time.Time) {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "month":
		start = time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	case "week":
		start = start.AddDate(0, 0, -int(start.Weekday()))
		return start, start.AddDate(0, 0, 7)
	default:
		return start, start.AddDate(0, 0, 1)
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

func scheduleTitle(ctx context.Context, period string, date, start time.Time) string {
	switch period {
	case "month":
		return start.Format("Jan, 2006")
	case "week":
		return displayDate(ctx, start) + " — " + displayDate(ctx, start.AddDate(0, 0, 6))
	default:
		return displayDate(ctx, date)
	}
}

func parseYearMonth(r *http.Request, now time.Time) (int, time.Month) {
	y, ye := strconv.Atoi(r.URL.Query().Get("year"))
	m, me := strconv.Atoi(r.URL.Query().Get("month"))
	if ye == nil && me == nil && y > 0 && y <= 9999 && m >= 1 && m <= 12 {
		return y, time.Month(m)
	}
	return now.Year(), now.Month()
}

func dispatchMatrixColumns(ctx context.Context, period string, start, end time.Time, loc *time.Location) []templates.DispatchMatrixColumn {
	var columns []templates.DispatchMatrixColumn
	if period == "day" {
		for hour := 0; hour < 24; hour++ {
			columns = append(columns, templates.DispatchMatrixColumn{Key: fmt.Sprintf("%02d", hour), Label: fmt.Sprintf("%02d:00", hour), Date: start.Format("2006-01-02"), Hour: hour})
		}
		return columns
	}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		columns = append(columns, templates.DispatchMatrixColumn{Key: d.Format("2006-01-02"), Label: displayDate(ctx, d), Date: d.Format("2006-01-02"), IsToday: isToday(d, loc)})
	}
	return columns
}

func buildMonthGrid(year int, month time.Month, jobs []templates.CalendarJob, loc *time.Location) []templates.WeekData {
	first := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	startDay := int(first.Weekday())
	daysInMonth := first.AddDate(0, 1, -1).Day()
	jobsByDay := map[int][]templates.CalendarJob{}
	for _, j := range jobs {
		jobsByDay[j.Day] = append(jobsByDay[j.Day], j)
	}
	var weeks []templates.WeekData
	day := 1
	for w := 0; w < 6 && day <= daysInMonth; w++ {
		var days []templates.DayData
		for d := 0; d < 7; d++ {
			if w == 0 && d < startDay || day > daysInMonth {
				days = append(days, templates.DayData{})
				continue
			}
			date := time.Date(year, month, day, 0, 0, 0, 0, loc)
			days = append(days, templates.DayData{DayNum: day, IsToday: isToday(date, loc), Date: date.Format("2006-01-02"), Jobs: jobsByDay[day]})
			day++
		}
		weeks = append(weeks, templates.WeekData{Days: days})
	}
	return weeks
}

func isToday(d time.Time, loc *time.Location) bool {
	n := time.Now().In(loc)
	return d.Year() == n.Year() && d.YearDay() == n.YearDay()
}
