package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func activityExportFilter(r *http.Request) (authorization.ActivityFilter, url.Values, error) {
	f := authorization.ActivityFilter{}
	values := url.Values{}
	if err := r.ParseForm(); err != nil {
		return f, values, err
	}
	f.ExportScope = authorization.Scope(r.Form.Get("scope"))
	// Avoid a named form control shadowing HTMLFormElement.action in shared
	// save/navigation handling, while retaining the canonical activity URL key.
	if supplied := r.Form["event_action"]; len(supplied) > 0 {
		if len(r.Form["action"]) > 0 {
			return f, values, authorization.ErrConflict
		}
		r.Form["action"] = supplied
	}
	for _, key := range []string{"type", "action"} {
		for _, v := range r.Form[key] {
			if v != "" {
				values.Add(key, v)
				if key == "type" {
					f.Types = append(f.Types, v)
				} else {
					f.Actions = append(f.Actions, v)
				}
			}
		}
	}
	if typ := r.Form.Get("object_type"); typ != "" {
		if len(f.Types) > 0 {
			return f, values, authorization.ErrConflict
		}
		f.Types = []string{typ}
		values.Set("object_type", typ)
	}
	for key, dest := range map[string]*int64{"object_id": &f.RecordID, "actor": &f.ActorID} {
		if v := r.Form.Get(key); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				return f, values, authorization.ErrConflict
			}
			*dest = id
			values.Set(key, v)
		}
	}
	for key, dest := range map[string]**time.Time{"from": &f.From, "until": &f.Until} {
		if v := r.Form.Get(key); v != "" {
			stamp, err := time.ParseInLocation("2006-01-02", v, middleware.CompanyLocation(r.Context()))
			if err != nil {
				return f, values, authorization.ErrConflict
			}
			*dest = &stamp
			values.Set(key, v)
		}
	}
	if conversion := r.Form.Get("conversion"); conversion != "" {
		if conversion != "1" || len(f.Types) != 1 || f.RecordID <= 0 || (f.Types[0] != "estimate" && f.Types[0] != "invoice") {
			return f, values, authorization.ErrConflict
		}
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			return f, values, authorization.ErrForbidden
		}
		if err := auth.Service.ParentFacet(r.Context(), auth.Actor, f.Types[0], f.RecordID, f.Types[0]+".provenance.view"); err != nil {
			return f, values, err
		}
		root := f.RecordID
		if f.Types[0] == "invoice" {
			if err := auth.Service.Pool.QueryRow(r.Context(), `SELECT estimate_id FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1`, f.RecordID).Scan(&root); err != nil {
				return f, values, authorization.ErrNotFound
			}
		}
		f.ProvenanceEstimateID = root
		f.Types = nil
		f.RecordID = 0
		values.Set("conversion", "1")
	}
	return f, values, authorization.ValidateActivityFilter(f)
}

func registerActivityExports(r chi.Router, pool *pgxpool.Pool) {
	svc := exports.Product(pool)
	r.Get("/activity/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		f, values, err := activityExportFilter(r)
		if err != nil {
			exportError(w, r, err)
			return
		}
		p, err := auth.Service.Principal(r.Context(), auth.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		d := activityExportDefinition(p, &f)
		values.Set("scope", string(f.ExportScope))
		raw, _ := json.Marshal(f)
		if err := d.Authorize(p, raw); err != nil {
			authorizationError(w, r, err)
			return
		}
		scope := "Assigned Jobs"
		if f.ExportScope == authorization.All {
			scope = "All Records"
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.ActivityExportReview(values, scope, newOperationKey()).Render(r.Context(), w)
	})
	r.Post("/activity/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		f, _, err := activityExportFilter(r)
		if err != nil {
			exportError(w, r, err)
			return
		}
		p, err := auth.Service.Principal(r.Context(), auth.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		d := activityExportDefinition(p, &f)
		raw, _ := json.Marshal(f)
		id, err := svc.Accept(r.Context(), auth.Actor, r.FormValue("operation_id"), d.Kind(), raw)
		if err != nil {
			exportError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
	})
}

func activityExportDefinition(p authorization.Principal, f *authorization.ActivityFilter) exports.Activity {
	if f.ExportScope == "" {
		f.ExportScope = authorization.Assigned
		if p.Allows("activity.export", authorization.All) {
			f.ExportScope = authorization.All
		}
	}
	return exports.Activity{ExportScope: f.ExportScope}
}
