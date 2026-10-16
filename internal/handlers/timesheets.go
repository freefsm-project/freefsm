package handlers

import (
	"errors"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func (h *TimeEntryHandler) report(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	filter, err := parseTimesheetFilter(r.URL.Query())
	if err != nil {
		http.Error(w, "Invalid Timesheet filter", 400)
		return
	}
	result, err := req.Service.Timesheet(r.Context(), req.Actor, filter)
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
