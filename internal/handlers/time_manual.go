package handlers

import (
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
	"net/http"
	"strconv"
)

func (h *TimeEntryHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	p, err := req.Service.Principal(r.Context(), req.Actor)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if !p.Allows("time.create", authorization.Own) && !p.Allows("time.create", authorization.Assigned) && !p.Allows("time.create", authorization.All) {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if r.Method == http.MethodGet {
		user, _ := middleware.UserFromContext(r.Context())
		jobs, err := h.allowedJobsForUser(r.Context(), user)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		subjects, err := req.Service.ManualTimeSubjects(r.Context(), req.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		data := templates.ManualTimePageData{UserID: p.UserID, Jobs: jobOptions(jobs)}
		for _, subject := range subjects {
			data.Subjects = append(data.Subjects, templates.UserRow{ID: subject.ID, Name: subject.Name})
		}
		render(w, r, templates.ManualTimeForm(data))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	for _, field := range []string{"latitude", "longitude", "gps", "accuracy", "captured_at"} {
		if r.Form.Has(field) {
			http.Error(w, "Manual entries cannot capture GPS", 400)
			return
		}
	}
	subject, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || subject <= 0 {
		http.Error(w, "Select a subject", 400)
		return
	}
	job, err := parseOptionalID(r.FormValue("job_id"))
	if err != nil || job < 0 {
		http.Error(w, "Select an authorized active Job", 400)
		return
	}
	start, err := parseTimeEntryEdit(r.FormValue("clock_in"), middleware.CompanyLocation(r.Context()))
	if err != nil {
		http.Error(w, "Provide an exact start with UTC offset", 400)
		return
	}
	end, err := parseTimeEntryEdit(r.FormValue("clock_out"), middleware.CompanyLocation(r.Context()))
	if err != nil {
		clockError(w, r, &authorization.ClockConflict{Code: "time_order"})
		return
	}
	id, err := req.Service.CreateManualTimeEntry(r.Context(), req.Actor, authorization.ManualTimeEntry{UserID: subject, JobID: job, ClockIn: &start, ClockOut: &end, Notes: r.FormValue("notes")})
	if err != nil {
		timeEntryError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/time-entries/%d", id), http.StatusSeeOther)
}
