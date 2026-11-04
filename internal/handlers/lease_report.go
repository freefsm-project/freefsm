package handlers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func (h *LeaseHandler) Report(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("lease_report_query_%d", authorizationActor(r).UserID)
	values := r.URL.Query()
	remembered := false
	if r.URL.RawQuery == "" {
		if cookie, err := r.Cookie(name); err == nil {
			if saved, err := base64.RawURLEncoding.DecodeString(cookie.Value); err == nil && len(saved) > 0 && len(saved) < 3000 {
				if parsed, err := url.ParseQuery(string(saved)); err == nil {
					values, remembered = parsed, true
				}
			}
		}
	}
	if values.Get("reset") != "" {
		values = url.Values{}
	}
	q := authorization.LeaseReportQuery{AsOf: values.Get("as_of"), Lifecycle: values.Get("lifecycle"), Search: values.Get("search"), Placement: values.Get("placement"), Billing: values.Get("billing"), Archive: values.Get("archive"), Sort: values.Get("sort")}
	invalid := false
	for name, destination := range map[string]*int64{"customer": &q.Customer, "asset": &q.Asset, "premises": &q.Premises} {
		if values.Get(name) != "" {
			value, err := strconv.ParseInt(values.Get(name), 10, 64)
			if err != nil {
				invalid = true
			}
			*destination = value
		}
	}
	for name, destination := range map[string]*int{"page": &q.Page, "per_page": &q.PerPage} {
		if values.Get(name) != "" {
			value, err := strconv.Atoi(values.Get(name))
			if err != nil {
				invalid = true
			}
			*destination = value
		}
	}
	var data authorization.LeaseReport
	var err error
	if invalid {
		err = authorization.ErrConflict
	} else {
		data, err = h.authority.LeaseReport(r.Context(), authorizationActor(r), q)
	}
	if err != nil {
		if remembered && (errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrConflict)) {
			http.SetCookie(w, &http.Cookie{Name: name, Path: "/leases/report", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: -1})
			http.Redirect(w, r, "/leases/report?reset=1", http.StatusSeeOther)
			return
		}
		if errors.Is(err, authorization.ErrConflict) {
			h.reportValidation(w, r, values)
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
	if remembered {
		http.Redirect(w, r, "/leases/report?"+values.Encode(), http.StatusSeeOther)
		return
	}
	for key := range values {
		switch key {
		case "as_of", "search", "lifecycle", "placement", "billing", "archive", "sort", "page", "per_page", "customer", "asset", "premises":
		default:
			values.Del(key)
		}
	}
	if saved := base64.RawURLEncoding.EncodeToString([]byte(values.Encode())); len(saved) < 3800 {
		http.SetCookie(w, &http.Cookie{Name: name, Value: saved, Path: "/leases/report", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: 365 * 24 * 60 * 60})
	}
	templates.LeaseReportPage(data).Render(r.Context(), w)
}

func (h *LeaseHandler) reportValidation(w http.ResponseWriter, r *http.Request, values url.Values) {
	// Reload current authority before reflecting any proposed sensitive field.
	data, err := h.authority.LeaseReport(r.Context(), authorizationActor(r), authorization.LeaseReportQuery{})
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	data.Rows, data.Total, data.RentCents = nil, 0, 0
	data.Inputs, data.Errors = map[string]string{}, map[string]string{}
	allowed := map[string]bool{"as_of": true, "search": true, "lifecycle": true, "sort": true, "page": true, "per_page": true, "customer": data.CanCustomer, "asset": data.CanAsset, "premises": data.CanPremises, "placement": data.CanAsset, "billing": data.CanInvoiceFilters, "archive": data.CanArchive}
	choices := map[string][]string{"lifecycle": {"", "draft", "scheduled", "active", "month_to_month", "terminated"}, "sort": {"", "number", "number_desc"}, "placement": {"", "unplaced", "wrong_premises"}, "billing": {"", "review", "void", "ungenerated"}, "archive": {"", "all", "archived"}}
	for key, visible := range allowed {
		if !visible {
			continue
		}
		value := values.Get(key)
		data.Inputs[key] = value
		if list, ok := choices[key]; ok && !slices.Contains(list, value) {
			data.Errors[key] = "Choose one of the listed options."
		}
		if key == "as_of" && value != "" {
			if _, err := time.Parse(time.DateOnly, value); err != nil || value > data.AsOf {
				data.Errors[key] = "Enter a company date in YYYY-MM-DD format no later than " + data.AsOf + "."
			}
		}
		if slices.Contains([]string{"customer", "asset", "premises", "page", "per_page"}, key) && value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 || key == "page" && n > 1000000 || key == "per_page" && n != 25 && n != 50 && n != 100 {
				data.Errors[key] = "Enter a positive whole number."
				if key == "page" {
					data.Errors[key] = "Enter a page from 1 through 1000000."
				} else if key == "per_page" {
					data.Errors[key] = "Choose 25, 50 or 100 rows per page."
				}
			}
		}
	}
	if len(data.Errors) == 0 {
		data.Errors["as_of"] = "Review the authorized filters and refresh, or clear filters to start again."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	templates.LeaseReportPage(data).Render(r.Context(), w)
}
