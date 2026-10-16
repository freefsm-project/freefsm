package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func (h *TimeEntryHandler) report(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	q := r.URL.Query()
	if q.Get("include_void") != "" && q.Get("include_void") != "true" && q.Get("include_void") != "false" {
		http.Error(w, "Invalid Void filter", 400)
		return
	}
	jobID := int64(0)
	standalone := q.Get("job_id") == "standalone"
	if q.Get("job_id") != "" && !standalone {
		var err error
		jobID, err = strconv.ParseInt(q.Get("job_id"), 10, 64)
		if err != nil || jobID <= 0 {
			http.Error(w, "Invalid Job filter", 400)
			return
		}
	}
	userID := int64(0)
	if q.Get("user_id") != "" {
		var err error
		userID, err = strconv.ParseInt(q.Get("user_id"), 10, 64)
		if err != nil || userID <= 0 {
			http.Error(w, "Invalid subject filter", 400)
			return
		}
	}
	result, err := req.Service.Timesheet(r.Context(), req.Actor, authorization.TimesheetFilter{DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), Search: q.Get("search"), UserID: userID, JobID: jobID, Standalone: standalone, Source: q.Get("source"), Lifecycle: q.Get("lifecycle"), GroupBy: q.Get("group_by"), Sort: q.Get("sort"), IncludeVoid: q.Get("include_void") == "true"})
	if err != nil {
		if errors.Is(err, authorization.ErrTimesheetFilter) {
			http.Error(w, err.Error(), 400)
			return
		}
		authorizationError(w, r, err)
		return
	}
	render(w, r, templates.TimesheetReport(result))
}
