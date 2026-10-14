package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func scheduleStateCookie(r *http.Request) string {
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		return "schedule_state"
	}
	return fmt.Sprintf("schedule_state_%d", u.ID)
}

// Presentation state is untrusted input, not cached records or authority. It is
// isolated per user, bounded, and validated exactly like an explicit URL.
func restoreScheduleState(r *http.Request) (*http.Request, bool) {
	if r.URL.RawQuery != "" {
		return r, false
	}
	c, err := r.Cookie(scheduleStateCookie(r))
	if err != nil || len(c.Value) > 4096 {
		return r, true
	}
	query, err := url.QueryUnescape(c.Value)
	if err != nil {
		return r, true
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return r, true
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = q.Encode()
	clone.URL = &u
	return clone, false
}

func rememberScheduleState(w http.ResponseWriter, r *http.Request, tab, period, date string) {
	q := url.Values{"tab": {tab}, "period": {period}, "date": {date}}
	for _, key := range []string{"q", "status", "assignment", "sort"} {
		q.Set(key, r.URL.Query().Get(key))
	}
	http.SetCookie(w, &http.Cookie{Name: scheduleStateCookie(r), Value: url.QueryEscape(q.Encode()), Path: "/schedule", MaxAge: 365 * 24 * 60 * 60, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
}

func scheduleFiltersValid(r *http.Request) bool {
	q := r.URL.Query()
	valid := func(value string, choices ...string) bool {
		for _, choice := range choices {
			if value == choice {
				return true
			}
		}
		return false
	}
	return len(q.Get("q")) <= 256 && valid(q.Get("status"), "", "all", "open", "finished") && valid(q.Get("assignment"), "", "all", "assigned", "unassigned") && valid(q.Get("sort"), "", "time", "title", "due")
}

func filterSchedule(r *http.Request, records []authorization.ScheduleRecord) []authorization.ScheduleRecord {
	q := r.URL.Query()
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	out := make([]authorization.ScheduleRecord, 0, len(records))
	for _, row := range records {
		finished := row.Category == "job:completed" || row.Category == "job:canceled"
		if q.Get("status") == "finished" && !finished || q.Get("status") == "open" && finished {
			continue
		}
		if q.Get("assignment") == "assigned" && len(row.AssigneeIDs) == 0 || q.Get("assignment") == "unassigned" && len(row.AssigneeIDs) > 0 {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(row.Title+" "+row.Customer+" "+row.Location+" "+row.Address+" "+strings.Join(row.Assignees, " ")), needle) {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch q.Get("sort") {
		case "title":
			if strings.ToLower(a.Title) != strings.ToLower(b.Title) {
				return strings.ToLower(a.Title) < strings.ToLower(b.Title)
			}
		case "due":
			if a.Due == nil && b.Due != nil {
				return false
			}
			if a.Due != nil && b.Due == nil {
				return true
			}
			if a.Due != nil && b.Due != nil && !a.Due.Equal(*b.Due) {
				return a.Due.Before(*b.Due)
			}
		default:
			if a.Start == nil && b.Start != nil {
				return false
			}
			if a.Start != nil && b.Start == nil {
				return true
			}
			if a.Start != nil && b.Start != nil && !a.Start.Equal(*b.Start) {
				return a.Start.Before(*b.Start)
			}
		}
		return a.ID < b.ID
	})
	return out
}
