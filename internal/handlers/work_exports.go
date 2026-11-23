package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func registerWorkExports(r chi.Router, pool *pgxpool.Pool) {
	for _, path := range []string{workloadPath + "/export", cohortPath + "/export", "/jobs/export", "/estimates/export", "/invoices/export"} {
		r.Get(path, workExportHandler(pool))
		r.Post(path, workExportHandler(pool))
	}
}

// RecoverySelection withholds both the kind and filters after access loss.
// Decode only the five versioned operational definitions owned by this UI.
func workExportRecoveryURL(r *http.Request, svc *exports.Service, id int64) string {
	auth, ok := authorization.FromContext(r.Context())
	if !ok {
		return ""
	}
	selection, err := svc.RecoverySelection(r.Context(), auth.Actor, id)
	if err != nil {
		return ""
	}
	for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned} {
		suffix := "-" + string(scope) + "-v1"
		if !strings.HasSuffix(selection.Kind, suffix) {
			continue
		}
		switch strings.TrimSuffix(selection.Kind, suffix) {
		case "workload":
			var f authorization.WorkloadFilter
			if json.Unmarshal(selection.Options, &f) != nil {
				return ""
			}
			return reportLink(workloadPath+"/export", workloadValues(f, scope), "", "")
		case "estimate-cohort":
			var f authorization.EstimateCohortFilter
			if json.Unmarshal(selection.Options, &f) != nil {
				return ""
			}
			return reportLink(cohortPath+"/export", cohortValues(f, scope), "", "")
		case "job-operational":
			var f authorization.JobOperationalFilter
			if json.Unmarshal(selection.Options, &f) != nil {
				return ""
			}
			values := documentListValues(authorization.DocumentListFilter{Search: f.Search, StatusID: f.StatusID, CustomerID: f.CustomerID, Sort: f.Sort}, scope)
			return reportLink("/jobs/export", values, "", "")
		case "estimate-list", "invoice-list":
			var f authorization.DocumentListFilter
			if json.Unmarshal(selection.Options, &f) != nil {
				return ""
			}
			kind := strings.TrimSuffix(strings.TrimSuffix(selection.Kind, suffix), "-list")
			return reportLink("/"+kind+"s/export", documentListValues(f, scope), "", "")
		}
	}
	return ""
}

func workExportHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		auth, ok := authorization.FromContext(r.Context())
		if !ok || auth.Actor.Channel == "api" {
			http.Error(w, "Forbidden", 403)
			return
		}
		kind := "job"
		if r.URL.Path == cohortPath+"/export" || r.URL.Path == "/estimates/export" {
			kind = "estimate"
		}
		if r.URL.Path == "/invoices/export" {
			kind = "invoice"
		}
		scope, err := workExportScope(r, kind)
		if err != nil {
			exportError(w, r, err)
			return
		}
		var d exports.Definition
		var options any
		var values url.Values
		title, description := "", "Current active operational records. All matching rows use the same filters and ordering as the list. No pricing or financial amounts are included."
		switch r.URL.Path {
		case workloadPath + "/export":
			f, e := parseWorkloadFilter(r.Form)
			err = e
			options = f
			values = workloadValues(f, scope)
			d = exports.Workload{ExportScope: scope}
			title = "Workload"
			description = "Current active Jobs: one row per distinct Job, all authorized assignees and measure flags. Assignee totals are nonadditive and are not repeated CSV rows."
		case cohortPath + "/export":
			f, e := parseEstimateCohortFilter(r.Form)
			err = e
			options = f
			values = cohortValues(f, scope)
			d = exports.EstimateCohorts{ExportScope: scope}
			title = "Estimate creation cohort"
			description = cohortDescription
		case "/jobs/export":
			f, e := parseJobOperationalFilter(r.Form)
			err = e
			options = f
			values = documentListValues(authorization.DocumentListFilter{Search: f.Search, StatusID: f.StatusID, CustomerID: f.CustomerID, Sort: f.Sort}, scope)
			d = exports.Jobs{ExportScope: scope}
			title = "Jobs"
		default:
			f, e := parseDocumentListFilter(kind, r.Form)
			err = e
			options = f
			values = documentListValues(f, scope)
			d = exports.Documents{ExportScope: scope, DocumentType: kind}
			title = kind + " list"
		}
		if err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		raw, err := json.Marshal(options)
		if err != nil {
			exportError(w, r, err)
			return
		}
		if r.Method == http.MethodPost {
			id, err := exports.Product(pool).Accept(r.Context(), auth.Actor, r.Form.Get("operation_id"), d.Kind(), raw)
			if err != nil {
				workExportFailure(w, r, err, title, description, string(scope), values, d.Columns())
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
			return
		}
		// Review checks selected references independently, including empty outputs.
		err = auth.Service.WithinSnapshotPermission(r.Context(), auth.Actor, d.Permission(), scope, func(tx pgx.Tx, p authorization.Principal) error {
			if err := d.Authorize(p, raw); err != nil {
				return err
			}
			selected, ok := d.(interface {
				AuthorizeSelection(context.Context, pgx.Tx, authorization.Principal, json.RawMessage) error
			})
			if !ok {
				return authorization.ErrForbidden
			}
			return selected.AuthorizeSelection(r.Context(), tx, p, raw)
		})
		if err != nil {
			workExportFailure(w, r, err, title, description, string(scope), values, d.Columns())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.WorkExportReview(title, r.URL.Path, description, string(scope), newOperationKey(), values, d.Columns(), "").Render(r.Context(), w)
	}
}

func workExportScope(r *http.Request, kind string) (authorization.Scope, error) {
	auth, ok := authorization.FromContext(r.Context())
	if !ok || auth.Actor.Channel == "api" {
		return "", authorization.ErrForbidden
	}
	p, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		return "", err
	}
	allowed := func(scope authorization.Scope) bool {
		if scope != authorization.All && scope != authorization.Assigned {
			return false
		}
		if !p.Allows(kind+".export", scope) || !p.Allows(kind+".view", scope) {
			return false
		}
		return r.URL.Path != cohortPath+"/export" || p.Allows("invoice.export", scope) && p.Allows("invoice.view", scope)
	}
	requested := authorization.Scope(r.Form.Get("scope"))
	if requested != "" {
		if !allowed(requested) {
			return "", authorization.ErrForbidden
		}
		return requested, nil
	}
	for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned} {
		if allowed(scope) {
			return scope, nil
		}
	}
	return "", authorization.ErrForbidden
}

func workExportFailure(w http.ResponseWriter, r *http.Request, err error, title, description, scope string, values url.Values, columns []string) {
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, exports.ErrConflict) {
		exportError(w, r, err)
		return
	}
	operation := r.Form.Get("operation_id")
	if operation == "" {
		operation = newOperationKey()
	}
	message := "Export unavailable. Your filters are retained. Retry the reviewed export when the service is available."
	if r.Method == http.MethodPost {
		message = "Export acceptance could not be confirmed. Check My CSV exports for its durable status, or retry this same reviewed operation; no duplicate export will be created."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	templates.WorkExportReview(title, r.URL.Path, description, scope, operation, values, columns, message).Render(r.Context(), w)
}
