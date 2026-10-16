package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func parseTimesheetFilter(q url.Values) (authorization.TimesheetFilter, error) {
	f := authorization.TimesheetFilter{DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), Search: q.Get("search"), Source: q.Get("source"), Lifecycle: q.Get("lifecycle"), GroupBy: q.Get("group_by"), Sort: q.Get("sort"), IncludeVoid: q.Get("include_void") == "true", Standalone: q.Get("job_id") == "standalone"}
	if v := q.Get("include_void"); v != "" && v != "true" && v != "false" {
		return f, authorization.ErrTimesheetFilter
	}
	for key, target := range map[string]*int64{"user_id": &f.UserID, "job_id": &f.JobID} {
		v := q.Get(key)
		if v == "" || key == "job_id" && f.Standalone {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return f, authorization.ErrTimesheetFilter
		}
		*target = id
	}
	return authorization.NormalizeTimesheetFilter(f)
}

func registerTimeExports(r chi.Router, pool *pgxpool.Pool) {
	svc := exports.Product(pool)
	handle := func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		f, err := parseTimesheetFilter(r.Form)
		if err != nil {
			http.Error(w, "Invalid export filters", 400)
			return
		}
		p, err := auth.Service.Principal(r.Context(), auth.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		kind := r.Form.Get("type")
		if kind != "" && kind != "time" && kind != "gps" {
			http.Error(w, "Invalid export type", 400)
			return
		}
		permission := "time.export"
		if kind == "gps" {
			permission = "time.gps.export"
		}
		scope := authorization.Scope(r.Form.Get("scope"))
		if scope == "" {
			for _, candidate := range []authorization.Scope{authorization.All, authorization.Own, authorization.Assigned} {
				if authorization.AuthorizeTimeExport(p, permission, candidate) == nil {
					scope = candidate
					break
				}
			}
		}
		d := exports.Timesheets{ExportScope: scope, GPS: kind == "gps"}
		raw, _ := json.Marshal(f)
		if err := d.Authorize(p, raw); err != nil {
			exportError(w, r, err)
			return
		}
		if r.Method == http.MethodPost {
			id, err := svc.Accept(r.Context(), auth.Actor, r.Form.Get("operation_id"), d.Kind(), raw)
			if err != nil {
				exportError(w, r, err)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
			return
		}
		values := url.Values{}
		for _, key := range []string{"date_from", "date_to", "search", "source", "lifecycle", "group_by", "sort", "user_id", "job_id", "include_void"} {
			if value := r.Form.Get(key); value != "" {
				values.Set(key, value)
			}
		}
		values.Set("scope", string(scope))
		values.Set("type", kind)
		values.Set("group_by", f.GroupBy)
		values.Set("sort", f.Sort)
		var scopes []string
		for _, candidate := range []authorization.Scope{authorization.All, authorization.Assigned, authorization.Own} {
			if authorization.AuthorizeTimeExport(p, permission, candidate) == nil {
				scopes = append(scopes, string(candidate))
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.TimeExportReview(values, string(scope), newOperationKey(), d.Columns(), d.GPS, scopes).Render(r.Context(), w)
	}
	r.Get("/time-entries/export", handle)
	r.Post("/time-entries/export", handle)
}
