package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/auditview"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func auditFilters(r *http.Request) auditview.Filters {
	sort := r.FormValue("sort")
	if sort == "" {
		sort = "newest"
	}
	return auditview.Filters{Permission: r.FormValue("permission"), Scope: r.FormValue("scope"), Channel: r.FormValue("channel"), Outcome: r.FormValue("outcome"), Role: r.FormValue("role"), Sort: sort}
}
func registerAudit(r chi.Router, pool *pgxpool.Pool) {
	r.Get("/settings/audit/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		p, err := auth.Service.Principal(r.Context(), auth.Actor)
		if err == nil {
			err = (exports.Audit{}).Authorize(p, nil)
		}
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		f := auditFilters(r)
		if err := f.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.AuditExportReview(f, newOperationKey()).Render(r.Context(), w)
	})
	r.Post("/settings/audit/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		raw, _ := json.Marshal(auditFilters(r))
		id, err := exports.Product(pool).Accept(r.Context(), auth.Actor, r.FormValue("operation_id"), (exports.Audit{}).Kind(), raw)
		if err != nil {
			exportError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), 303)
	})
	r.Get("/settings/audit", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		f := auditFilters(r)
		after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
		size := 25
		if r.FormValue("per_page") != "" {
			size, _ = strconv.Atoi(r.FormValue("per_page"))
		}
		page, err := auditview.Browse(r.Context(), pool, auth.Actor, f, after, size)
		if err != nil {
			if errors.Is(err, auditview.ErrFilter) {
				http.Error(w, err.Error(), 400)
			} else {
				authorizationError(w, r, err)
			}
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.AuditPage(page, f, size).Render(r.Context(), w)
	})
}
