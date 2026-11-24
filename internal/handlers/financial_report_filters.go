package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

const agingReportPath = "/reports/invoice-aging"
const paymentsCreditsReportPath = "/reports/payments-credits"

var errFinancialReportView = errors.New("invalid financial report view")

type financialReportView struct {
	Value, Label string
	Kind         authorization.FinancialReportKind
}

func parseFinancialReportFilter(kind authorization.FinancialReportKind, q url.Values) (services.FinancialReportFilter, error) {
	f := services.FinancialReportFilter{}
	allowed := map[string]bool{"view": true, "customer_id": true, "sort": true}
	if kind == authorization.FinancialAging {
		allowed["bucket"] = true
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined {
		allowed["from"], allowed["to"] = true, true
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined || kind == authorization.FinancialCredit {
		allowed["include_archived"] = true
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return f, services.ErrFinancialReportFilter
		}
	}
	if raw := q.Get("customer_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return f, services.ErrFinancialReportFilter
		}
		f.CustomerID = id
	}
	if raw := q.Get("include_archived"); raw != "" {
		if raw != "true" && raw != "false" {
			return f, services.ErrFinancialReportFilter
		}
		f.IncludeArchived = raw == "true"
	}
	f.From, f.To, f.Sort, f.Bucket = q.Get("from"), q.Get("to"), q.Get("sort"), q.Get("bucket")
	return services.NormalizeFinancialReportFilter(kind, f)
}

func financialFilterValues(kind authorization.FinancialReportKind, f services.FinancialReportFilter) url.Values {
	q := url.Values{"sort": {f.Sort}}
	if f.CustomerID != 0 {
		q.Set("customer_id", strconv.FormatInt(f.CustomerID, 10))
	}
	if kind == authorization.FinancialAging {
		q.Set("bucket", f.Bucket)
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined {
		q.Set("from", f.From)
		q.Set("to", f.To)
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined || kind == authorization.FinancialCredit {
		q.Set("include_archived", strconv.FormatBool(f.IncludeArchived))
	}
	return q
}

func financialPageViews(path string) []financialReportView {
	switch path {
	case agingReportPath:
		return []financialReportView{{"invoices", "Invoice Aging", authorization.FinancialAging}, {"customers", "Customer financial", authorization.FinancialCustomers}}
	case paymentsCreditsReportPath:
		return []financialReportView{{"combined", "Payments/Credits", authorization.FinancialCombined}, {"settlements", "Settlement activity", authorization.FinancialSettlement}, {"credits", "Current Credit", authorization.FinancialCredit}}
	default:
		return nil
	}
}

// Resolve explicit intent before any dependency calls, so failures retain it.
func financialRequestedView(path string, q url.Values) (financialReportView, error) {
	if len(q["view"]) > 1 {
		return financialReportView{}, errFinancialReportView
	}
	views := financialPageViews(path)
	if q.Get("view") == "" && len(views) > 0 {
		return financialReportView{}, nil
	}
	for _, view := range views {
		if q.Get("view") == view.Value {
			return view, nil
		}
	}
	return financialReportView{}, errFinancialReportView
}

// An explicit denied view never falls back to a different financial facet.
// q must be the successfully decoded query used for filter parsing as well.
func financialView(r *http.Request, q url.Values) (financialReportView, []templates.WorkReportOption, error) {
	auth, ok := authorization.FromContext(r.Context())
	if !ok || auth.Actor.Channel == "api" {
		return financialReportView{}, nil, authorization.ErrForbidden
	}
	intent, err := financialRequestedView(r.URL.Path, q)
	if err != nil {
		return financialReportView{}, nil, err
	}
	requested := intent.Value
	views := financialPageViews(r.URL.Path)
	p, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		return financialReportView{}, nil, err
	}
	var selected financialReportView
	var options []templates.WorkReportOption
	for _, view := range views {
		if authorization.AuthorizeFinancialReport(p, view.Kind, false) != nil {
			continue
		}
		options = append(options, templates.WorkReportOption{Value: view.Value, Label: view.Label})
		if requested == view.Value || requested == "" && selected.Value == "" {
			selected = view
		}
	}
	if selected.Value == "" {
		return selected, nil, authorization.ErrForbidden
	}
	return selected, options, nil
}
