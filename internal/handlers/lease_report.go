package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func (h *LeaseHandler) Report(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	q := authorization.LeaseReportQuery{AsOf: values.Get("as_of"), Lifecycle: values.Get("lifecycle"), Search: values.Get("search"), Placement: values.Get("placement"), Billing: values.Get("billing"), Archive: values.Get("archive"), Sort: values.Get("sort")}
	for name, destination := range map[string]*int64{"customer": &q.Customer, "asset": &q.Asset, "premises": &q.Premises} {
		if values.Get(name) != "" {
			value, err := strconv.ParseInt(values.Get(name), 10, 64)
			if err != nil {
				http.Error(w, "Invalid report filter", 400)
				return
			}
			*destination = value
		}
	}
	for name, destination := range map[string]*int{"page": &q.Page, "per_page": &q.PerPage} {
		if values.Get(name) != "" {
			value, err := strconv.Atoi(values.Get(name))
			if err != nil {
				http.Error(w, "Invalid report page", 400)
				return
			}
			*destination = value
		}
	}
	data, err := h.authority.LeaseReport(r.Context(), authorizationActor(r), q)
	if err != nil {
		if errors.Is(err, authorization.ErrConflict) {
			http.Error(w, "Select valid report filters and a company date no later than today", 400)
			return
		}
		if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
			authorizationError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		templates.LeaseReportFailure(q).Render(r.Context(), w)
		return
	}
	templates.LeaseReportPage(data).Render(r.Context(), w)
}
