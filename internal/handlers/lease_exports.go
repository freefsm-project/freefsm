package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const leaseExportPath = "/leases/report/export"

func leaseExportValues(f authorization.LeaseExportFilter) url.Values {
	v := url.Values{"as_of": {f.AsOf}, "search": {f.Search}, "lifecycle": {f.Lifecycle}, "ending_from": {f.EndingFrom}, "ending_through": {f.EndingThrough}, "archive": {f.Archive}, "placement": {f.Placement}, "billing": {f.Billing}, "sort": {f.Sort}}
	for key, id := range map[string]int64{"customer": f.Customer, "asset": f.Asset, "premises": f.Premises} {
		if id > 0 {
			v.Set(key, strconv.FormatInt(id, 10))
		}
	}
	return v
}

func parseLeaseExportFilter(v url.Values) (authorization.LeaseExportFilter, error) {
	f := authorization.LeaseExportFilter{AsOf: v.Get("as_of"), Search: v.Get("search"), Lifecycle: v.Get("lifecycle"), EndingFrom: v.Get("ending_from"), EndingThrough: v.Get("ending_through"), Archive: v.Get("archive"), Placement: v.Get("placement"), Billing: v.Get("billing"), Sort: v.Get("sort")}
	for key, target := range map[string]*int64{"customer": &f.Customer, "asset": &f.Asset, "premises": &f.Premises} {
		if value := v.Get(key); value != "" {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				return f, authorization.ErrConflict
			}
			*target = id
		}
	}
	return authorization.NormalizeLeaseExportFilter(f)
}

func registerLeaseExports(r chi.Router, pool *pgxpool.Pool) {
	handle := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		auth, ok := authorization.FromContext(r.Context())
		if !ok || auth.Actor.Channel == "api" {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || middleware.DecodeFinancialExportForm(r) != nil || r.ParseForm() != nil {
			http.Error(w, "Invalid export filters", http.StatusBadRequest)
			return
		}
		for key, values := range r.PostForm {
			query[key] = append(query[key], values...)
		}
		for key, values := range query {
			if len(values) != 1 {
				http.Error(w, "Invalid export filters", http.StatusBadRequest)
				return
			}
			switch key {
			case "mode", "operation_id", "csrf_token", "as_of", "search", "lifecycle", "customer", "asset", "premises", "ending_from", "ending_through", "archive", "placement", "billing", "sort":
			default:
				http.Error(w, "Invalid export filters", http.StatusBadRequest)
				return
			}
		}
		mode := authorization.LeaseExportMode(query.Get("mode"))
		if mode == "" && r.Method == http.MethodGet {
			mode = authorization.LeaseExportOperational
		}
		if mode != authorization.LeaseExportOperational && mode != authorization.LeaseExportBilling {
			http.Error(w, "Choose operational or Billing export mode", http.StatusBadRequest)
			return
		}
		f, err := parseLeaseExportFilter(query)
		if err != nil {
			leaseExportValidation(w, r, mode, query, "Review dates, the inclusive ending range, numeric IDs and listed filter choices.")
			return
		}
		d := exports.Leases{Mode: mode}
		raw, _ := json.Marshal(f)
		// Check selected identities even for an empty result before displaying intent.
		err = auth.Service.WithinSnapshotPermission(r.Context(), auth.Actor, d.Permission(), authorization.All, func(tx pgx.Tx, p authorization.Principal) error {
			if err := d.AuthorizeSelection(r.Context(), tx, p, raw); err != nil {
				return err
			}
			if r.Method == http.MethodGet {
				return authorization.ValidateLeaseExportFreshSelection(r.Context(), tx, p, mode, f)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, authorization.ErrConflict) {
				leaseExportValidation(w, r, mode, query, "Use a company date no later than today. Pending and Failed require today's company date: historical worker-queue history was not retained. Review the ending range and filters; missing retained agreement history may prevent complete selection.")
			} else {
				exportError(w, r, err)
			}
			return
		}
		values := leaseExportValues(f)
		back := "/leases/report?" + values.Encode()
		values.Set("mode", string(mode))
		p := templates.LeaseExportReviewData{Mode: string(mode), Kind: d.Kind(), Filters: values, Columns: d.Columns(), BackURL: back, Operation: newOperationKey()}
		if r.Method == http.MethodPost {
			p.Operation = query.Get("operation_id")
			id, err := exports.Product(pool).Accept(r.Context(), auth.Actor, p.Operation, d.Kind(), raw)
			if err == nil {
				http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
				return
			}
			if errors.Is(err, authorization.ErrConflict) {
				leaseExportValidation(w, r, mode, query, "This selection is no longer eligible for a new export. Pending and Failed require today's company date. Review the dates and authorized filters; they have not been changed automatically.")
				return
			}
			if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, exports.ErrConflict) {
				exportError(w, r, err)
				return
			}
			p.Error = "Export acceptance could not be confirmed. Check My CSV exports for durable status, or retry this same operation; no duplicate export will be created."
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		templates.LeaseExportReview(p).Render(r.Context(), w)
	}
	r.Get(leaseExportPath, handle)
	r.Post(leaseExportPath, handle)
}

// Invalid selections are reflected only after current base/mode authority and
// independent selected-record checks. Date/enum correction never clears intent.
func leaseExportValidation(w http.ResponseWriter, r *http.Request, mode authorization.LeaseExportMode, proposed url.Values, message string) {
	auth, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	values := url.Values{"mode": {string(mode)}}
	err := auth.Service.WithinSnapshotPermission(r.Context(), auth.Actor, "lease.export", authorization.All, func(tx pgx.Tx, p authorization.Principal) error {
		base, err := authorization.NormalizeLeaseExportFilter(authorization.LeaseExportFilter{})
		if err != nil {
			return err
		}
		if err := authorization.AuthorizeLeaseExport(p, mode, base); err != nil {
			return err
		}
		for _, key := range []string{"as_of", "ending_from", "ending_through", "search", "lifecycle", "sort"} {
			values.Set(key, proposed.Get(key))
		}
		for key, permission := range map[string]string{"customer": "customer.view", "asset": "asset.view", "premises": "location.view", "placement": "asset.view", "archive": "lease.archived.view"} {
			if !p.Allows(permission, authorization.All) {
				if proposed.Get(key) != "" {
					return authorization.ErrForbidden
				}
				continue
			}
			if key == "customer" || key == "asset" || key == "premises" {
				if id, err := strconv.ParseInt(proposed.Get(key), 10, 64); err == nil && id > 0 {
					selected := base
					switch key {
					case "customer":
						selected.Customer = id
					case "asset":
						selected.Asset = id
					case "premises":
						selected.Premises = id
					}
					if err := authorization.AuthorizeLeaseExportSelection(r.Context(), tx, p, mode, selected); err != nil {
						return err
					}
				}
			}
			values.Set(key, proposed.Get(key))
		}
		if proposed.Get("billing") != "" {
			if mode == authorization.LeaseExportOperational {
				return authorization.ErrForbidden
			}
			selected := base
			selected.Billing = proposed.Get("billing")
			if err := authorization.AuthorizeLeaseExport(p, mode, selected); errors.Is(err, authorization.ErrForbidden) {
				return err
			}
		}
		if mode == authorization.LeaseExportBilling && p.Allows("invoice.view", authorization.Assigned) && p.Allows("pricing.view", authorization.Assigned) {
			values.Set("billing", proposed.Get("billing"))
		}
		return nil
	})
	if err != nil {
		exportError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	templates.LeaseExportInvalid(values, message).Render(r.Context(), w)
}

func leaseExportSelectionURL(selection exports.RecoverySelection) string {
	mode := ""
	switch selection.Kind {
	case exports.LeaseOperationalKind:
		mode = string(authorization.LeaseExportOperational)
	case exports.LeaseBillingKind:
		mode = string(authorization.LeaseExportBilling)
	default:
		return ""
	}
	f, err := exports.DecodeLeaseExportFilter(selection.Options)
	if err != nil {
		return ""
	}
	v := leaseExportValues(f)
	v.Set("mode", mode)
	return leaseExportPath + "?" + v.Encode()
}

func leaseExportControls(p authorization.Principal, q authorization.LeaseReportQuery) []templates.LeaseExportLink {
	f := authorization.LeaseExportFilter{AsOf: q.AsOf, Search: q.Search, Lifecycle: q.Lifecycle, Customer: q.Customer, Asset: q.Asset, Premises: q.Premises, EndingFrom: q.EndingFrom, EndingThrough: q.EndingThrough, Archive: q.Archive, Placement: q.Placement, Billing: q.Billing, Sort: q.Sort}
	var err error
	f, err = authorization.NormalizeLeaseExportFilter(f)
	if err != nil {
		return nil
	}
	var links []templates.LeaseExportLink
	for _, mode := range []authorization.LeaseExportMode{authorization.LeaseExportOperational, authorization.LeaseExportBilling} {
		if authorization.AuthorizeLeaseExport(p, mode, f) != nil {
			continue
		}
		v := leaseExportValues(f)
		v.Set("mode", string(mode))
		label := "Review operational CSV export"
		if mode == authorization.LeaseExportBilling {
			label = "Review Billing CSV export"
		}
		links = append(links, templates.LeaseExportLink{Text: label, URL: leaseExportPath + "?" + v.Encode()})
	}
	return links
}
