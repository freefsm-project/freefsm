package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type financialExportDefinition interface {
	exports.Definition
	AuthorizeSelection(context.Context, pgx.Tx, authorization.Principal, json.RawMessage) error
}

type financialExportSpec struct {
	Kind                                 authorization.FinancialReportKind
	Path, Page, View, Title, Description string
	Definition                           financialExportDefinition
}

func financialExportSpecs() []financialExportSpec {
	return []financialExportSpec{
		{authorization.FinancialAging, agingReportPath + "/export", agingReportPath, "invoices", "Invoice Aging", "Eligible current Invoice receivables and aging totals. Available Credit is not subtracted.", exports.InvoiceAging{}},
		{authorization.FinancialCustomers, "/reports/customer-financials/export", agingReportPath, "customers", "Customer financial", "Current Customer receivables and aging summaries from authorized eligible Invoices; no Credit or settlement history.", exports.CustomerFinancials{}},
		{authorization.FinancialSettlement, "/reports/settlements/export", paymentsCreditsReportPath, "settlements", "Settlement activity", "Immutable activity, compensating events and source allocations. No current Credit balance.", exports.SettlementActivity{}},
		{authorization.FinancialCredit, "/reports/credits/export", paymentsCreditsReportPath, "credits", "Current Credit", "Current Available Credit sources with intrinsic Payment identities; no receipt details or settlement history.", exports.CurrentCredit{}},
		{authorization.FinancialCombined, paymentsCreditsReportPath + "/export", paymentsCreditsReportPath, "combined", "Payments/Credits", "Settlement activity and separately identified current Available Credit from one snapshot. Both financial facets are required.", exports.PaymentsCredits{}},
	}
}

func registerFinancialExports(r chi.Router, pool *pgxpool.Pool) {
	for _, spec := range financialExportSpecs() {
		r.Get(spec.Path, financialExportHandler(pool, spec))
		r.Post(spec.Path, financialExportHandler(pool, spec))
	}
}

func financialExportHandler(pool *pgxpool.Pool, spec financialExportSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "Invalid export filters", http.StatusBadRequest)
			return
		}
		auth, ok := authorization.FromContext(r.Context())
		if !ok || auth.Actor.Channel == "api" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := middleware.DecodeFinancialExportForm(r); err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		// Do not reuse r.Form's query portion: middleware may have discarded a
		// decoding error or cached a partial query. Merge the validated query.
		form := query
		for key, entries := range r.PostForm {
			form[key] = append(form[key], entries...)
		}
		values := url.Values{}
		for key, entries := range form {
			if len(entries) != 1 {
				http.Error(w, "Invalid export filters", 400)
				return
			}
			if key == "operation_id" || key == "csrf_token" {
				continue
			}
			if key == "view" {
				http.Error(w, "Invalid export filters", 400)
				return
			}
			values[key] = append([]string(nil), entries...)
		}
		f, err := parseFinancialReportFilter(spec.Kind, values)
		if err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		raw, err := json.Marshal(f)
		if err != nil {
			exportError(w, r, err)
			return
		}
		if _, err := exports.ParseFinancialExportFilter(spec.Kind, raw); err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		values = financialFilterValues(spec.Kind, f)
		p := templates.FinancialExportReviewData{Title: spec.Title, Path: spec.Path, Description: spec.Description, Filters: values, Columns: spec.Definition.Columns(), BackURL: reportLink(spec.Page, values, "view", spec.View), Operation: form.Get("operation_id")}
		if p.Operation == "" {
			p.Operation = newOperationKey()
		}
		if r.Method == http.MethodPost {
			id, err := exports.Product(pool).Accept(r.Context(), auth.Actor, form.Get("operation_id"), spec.Definition.Kind(), raw)
			if err != nil {
				financialExportFailure(w, r, p, err)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
			return
		}
		// GET reviews always create a fresh intent; regeneration cannot replay an old operation.
		p.Operation = newOperationKey()
		err = auth.Service.WithinSnapshotPermission(r.Context(), auth.Actor, spec.Definition.Permission(), authorization.All, func(tx pgx.Tx, principal authorization.Principal) error {
			return spec.Definition.AuthorizeSelection(r.Context(), tx, principal, raw)
		})
		if err != nil {
			financialExportFailure(w, r, p, err)
			return
		}
		templates.FinancialExportReview(p).Render(r.Context(), w)
	}
}

func financialExportFailure(w http.ResponseWriter, r *http.Request, p templates.FinancialExportReviewData, err error) {
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, exports.ErrConflict) {
		exportError(w, r, err)
		return
	}
	p.Error = "Export unavailable. Your filters are retained; retry the reviewed export when the service is available."
	if r.Method == http.MethodPost {
		p.Error = "Export acceptance could not be confirmed. Check My CSV exports for its durable status, or retry this same reviewed operation; no duplicate export will be created."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	templates.FinancialExportReview(p).Render(r.Context(), w)
}

func financialExportControls(principal authorization.Principal, report services.FinancialReport) []templates.FinancialCell {
	var links []templates.FinancialCell
	for _, spec := range financialExportSpecs() {
		if spec.Kind != report.Kind {
			continue
		}
		raw, err := json.Marshal(report.Filter)
		if err != nil || spec.Definition.Authorize(principal, raw) != nil {
			continue
		}
		links = append(links, templates.FinancialCell{Text: "Review " + spec.Title + " CSV export", URL: reportLink(spec.Path, financialFilterValues(spec.Kind, report.Filter), "", "")})
	}
	return links
}

func financialExportSelectionURL(selection exports.RecoverySelection) string {
	for _, spec := range financialExportSpecs() {
		if spec.Definition.Kind() != selection.Kind {
			continue
		}
		f, err := exports.ParseFinancialExportFilter(spec.Kind, selection.Options)
		if err != nil {
			return ""
		}
		return reportLink(spec.Path, financialFilterValues(spec.Kind, f), "", "")
	}
	return ""
}

// Resolve authorized recovery intent once. Definition-specific decoders must
// never read stored options directly or infer intent from redacted status data.
func exportRecoveryURL(r *http.Request, svc *exports.Service, id int64) string {
	auth, ok := authorization.FromContext(r.Context())
	if !ok || auth.Actor.Channel == "api" {
		return ""
	}
	selection, err := svc.RecoverySelection(r.Context(), auth.Actor, id)
	if err != nil {
		return ""
	}
	if recovery := workExportSelectionURL(selection); recovery != "" {
		return recovery
	}
	return financialExportSelectionURL(selection)
}
