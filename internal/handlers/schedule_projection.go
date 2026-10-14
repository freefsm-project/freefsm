package handlers

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func (h *ScheduleHandler) showSchedule(w http.ResponseWriter, r *http.Request, tab, period string, phoneDefault, savePreference bool) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if tab == "dispatch" && !scheduleDispatchVisible(r) {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if !scheduleFiltersValid(r) {
		http.Error(w, "Invalid schedule filters. Choose a listed status, assignment or sort and a search of at most 256 bytes.", 400)
		return
	}
	loc := middleware.CompanyLocation(r.Context())
	date := parseDateParam(r, "date", loc)
	if r.URL.Query().Has("year") {
		y, m := parseYearMonth(r, date)
		date = time.Date(y, m, 1, 0, 0, 0, 0, loc)
	}
	if period == "month" {
		date = time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, loc)
	}
	start, end := dispatchRange(period, date, loc)
	prev, next := dispatchPrevNext(period, date)
	p := templates.SchedulePageData{Title: scheduleTitle(r.Context(), period, date, start), Tab: tab, Period: period, Date: date.Format("2006-01-02"), PrevDate: prev.Format("2006-01-02"), NextDate: next.Format("2006-01-02"), TileURL: h.mapTileURL(r)}
	p.PhoneDefault = phoneDefault
	p.UpdatedAt = time.Now().In(loc).Format("2006-01-02 15:04:05 MST")
	p.Query = r.URL.Query().Get("q")
	p.StatusFilter = r.URL.Query().Get("status")
	p.AssignmentFilter = r.URL.Query().Get("assignment")
	p.Sort = r.URL.Query().Get("sort")
	snapshot, err := req.Service.Schedule(r.Context(), req.Actor, start, end)
	if err != nil {
		if err == authorization.ErrForbidden || err == authorization.ErrNotFound {
			authorizationError(w, r, err)
			return
		}
		p.Error = "Schedule unavailable. Retry to load current authorized Jobs."
		slog.Error("schedule unavailable", "path", r.URL.Path, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = templates.SchedulePage(p).Render(r.Context(), w)
		return
	}
	records := filterSchedule(r, snapshot.Jobs)
	if savePreference {
		if u, _ := middleware.UserFromContext(r.Context()); u != nil {
			if err := h.userSvc.UpdateSchedulePreferences(r.Context(), u.ID, tab, period); err != nil {
				slog.Error("update schedule preferences", "error", err, "user_id", u.ID)
			}
		}
	}
	p.Count = len(records)
	rememberScheduleState(w, r, tab, period, p.Date)
	p.IsList = tab == "list"
	p.IsDispatch = tab == "dispatch"
	p.IsMap = tab == "map"
	p.IsMonth = tab == "calendar" && period == "month"
	p.IsWeek = tab == "calendar" && period == "week"
	p.IsDay = tab == "calendar" && period == "day"
	p.PrevYear = prev.Year()
	p.PrevMonth = int(prev.Month())
	p.NextYear = next.Year()
	p.NextMonth = int(next.Month())
	for _, row := range records {
		card := scheduleCard(r, row)
		if card.NeedsRepair {
			p.RepairJobs = append(p.RepairJobs, card)
			continue
		}
		if row.Start == nil || row.End == nil || !row.End.After(*row.Start) {
			p.UnscheduledJobs = append(p.UnscheduledJobs, card)
			continue
		}
		p.Jobs = append(p.Jobs, card)
		if card.Mapped {
			p.MapJobs = append(p.MapJobs, card)
		} else {
			p.UnmappedJobs = append(p.UnmappedJobs, card)
		}
	}
	if tab == "calendar" {
		for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
			d := templates.ScheduleDay{Date: day.Format("2006-01-02"), DayName: day.Format("Mon"), DayNum: day.Day(), IsToday: isToday(day, loc)}
			for _, card := range p.Jobs {
				if card.IntervalStart.Before(day.AddDate(0, 0, 1)) && card.IntervalEnd.After(day) {
					clipped := card
					clipped.Day = day.Day()
					clipped.Hour = 0
					if card.IntervalStart.After(day) {
						clipped.Hour = card.IntervalStart.In(loc).Hour()
					}
					d.Jobs = append(d.Jobs, clipped)
				}
			}
			p.Days = append(p.Days, d)
		}
		if p.IsDay && len(p.Days) > 0 {
			p.Jobs = p.Days[0].Jobs
		}
		if p.IsMonth {
			var repeated []templates.CalendarJob
			for _, day := range p.Days {
				repeated = append(repeated, day.Jobs...)
			}
			p.Weeks = buildMonthGrid(start.Year(), start.Month(), repeated, loc)
		}
	}
	if p.IsDispatch {
		for _, user := range snapshot.Directory {
			p.Techs = append(p.Techs, templates.ScheduleTech{ID: user.ID, Name: user.Name})
		}
		seen := map[int64]bool{}
		for _, tech := range p.Techs {
			seen[tech.ID] = true
		}
		for _, record := range records {
			for i, id := range record.AssigneeIDs {
				if !seen[id] && i < len(record.Assignees) {
					p.Techs = append(p.Techs, templates.ScheduleTech{ID: id, Name: record.Assignees[i]})
					seen[id] = true
				}
			}
		}
		sort.Slice(p.Techs, func(i, j int) bool {
			if p.Techs[i].Name == p.Techs[j].Name {
				return p.Techs[i].ID < p.Techs[j].ID
			}
			return p.Techs[i].Name < p.Techs[j].Name
		})
		columns := dispatchMatrixColumns(r.Context(), period, start, end, loc)
		for _, tech := range p.Techs {
			row := templates.DispatchMatrixRow{Tech: tech}
			for _, col := range columns {
				cell := templates.DispatchMatrixCell{Column: col}
				day, _ := time.ParseInLocation("2006-01-02", col.Date, loc)
				for _, card := range p.Jobs {
					hour := card.Hour
					if card.IntervalStart.Before(day) {
						hour = 0
					}
					intersects := card.IntervalStart.Before(day.AddDate(0, 0, 1)) && card.IntervalEnd.After(day)
					for _, id := range card.AssigneeIDs {
						if id == tech.ID && intersects && (period != "day" || hour == col.Hour) {
							cell.Jobs = append(cell.Jobs, card)
						}
					}
				}
				row.Cells = append(row.Cells, cell)
			}
			p.DispatchMatrix.Rows = append(p.DispatchMatrix.Rows, row)
		}
		p.DispatchMatrix.Columns = columns
		p.DispatchMatrix.Period = period
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = templates.SchedulePage(p).Render(r.Context(), w)
}

func scheduleCard(r *http.Request, row authorization.ScheduleRecord) templates.CalendarJob {
	loc := middleware.CompanyLocation(r.Context())
	c := templates.CalendarJob{ID: row.ID, JobType: row.Title, Customer: row.Customer, Address: strings.TrimSpace(row.Location + " " + row.Address), StatusName: row.Status, StatusColor: row.Color, Category: strings.TrimPrefix(row.Category, "job:"), Assignee: strings.Join(row.Assignees, ", "), AssigneeIDs: row.AssigneeIDs, Conflict: row.Conflict, Duration: 1}
	c.InvoiceID = row.InvoiceID
	c.InvoiceStatusName = row.InvoiceStatus
	c.InvoiceStatusColor = row.InvoiceColor
	if c.Customer == "" {
		c.Customer = "Customer context unavailable"
	}
	if c.Assignee == "" {
		c.Assignee = "Unassigned"
	}
	if c.StatusName == "" {
		c.StatusName = "Status not recorded"
	}
	if c.Address == "" {
		if row.HasLocation {
			c.Address = "Location context unavailable"
		} else {
			c.Address = "Missing Location — link a service Location"
		}
	}
	if row.Start == nil && row.End == nil {
		c.TimeRange = "Unscheduled"
	} else if row.Start == nil || row.End == nil || !row.End.After(*row.Start) {
		c.NeedsRepair = true
		start, end := "Start not recorded", "End not recorded"
		if row.Start != nil {
			start = row.Start.In(loc).Format(time.RFC3339)
		}
		if row.End != nil {
			end = row.End.In(loc).Format(time.RFC3339)
		}
		c.TimeRange = "Schedule needs reviewed repair — incomplete or invalid interval: " + start + " — " + end
	} else {
		c.IntervalStart = *row.Start
		c.IntervalEnd = *row.End
		c.StartTime = row.Start.In(loc).Format("2006-01-02 15:04 MST")
		c.EndTime = row.End.In(loc).Format("2006-01-02 15:04 MST")
		c.TimeRange = c.StartTime + " — " + c.EndTime
		c.Time = c.TimeRange
		c.DateISO = row.Start.In(loc).Format("2006-01-02")
		c.Day = row.Start.In(loc).Day()
		c.Hour = row.Start.In(loc).Hour()
	}
	if row.ArrivalStart != nil && row.ArrivalEnd != nil {
		c.Arrival = fmt.Sprintf("Arrival %s — %s", row.ArrivalStart.In(loc).Format("2006-01-02 15:04 MST"), row.ArrivalEnd.In(loc).Format("2006-01-02 15:04 MST"))
		if row.Start == nil || row.ArrivalStart.After(*row.Start) || row.ArrivalEnd.Before(*row.Start) {
			c.Arrival += " — needs reviewed repair"
			c.NeedsRepair = true
		}
	} else if row.ArrivalStart != nil || row.ArrivalEnd != nil {
		c.Arrival = "Arrival window needs reviewed repair — incomplete recorded window"
		c.NeedsRepair = true
	} else {
		c.Arrival = "No arrival window"
	}
	c.Overdue = row.Due != nil && row.Due.Format("2006-01-02") < time.Now().In(loc).Format("2006-01-02") && row.Category != "job:completed" && row.Category != "job:canceled"
	if row.Latitude != nil && row.Longitude != nil && !math.IsNaN(*row.Latitude) && !math.IsNaN(*row.Longitude) && math.Abs(*row.Latitude) <= 90 && math.Abs(*row.Longitude) <= 180 {
		c.Mapped = true
		c.Lat = *row.Latitude
		c.Lng = *row.Longitude
	} else if !row.HasLocation {
		c.MapReason = "Missing Location — link a service Location"
	} else if row.Location == "" {
		c.MapReason = "Location context unavailable"
	} else if row.Latitude != nil && row.Longitude != nil {
		c.MapReason = "Invalid coordinates — review the Location coordinates"
	} else {
		c.MapReason = "Missing coordinates — review the Location coordinates"
	}
	return c
}
