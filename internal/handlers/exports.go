package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func registerExports(r chi.Router, pool *pgxpool.Pool) {
	svc := exports.Product(pool)
	r.Get("/exports", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		statuses, err := svc.List(r.Context(), auth.Actor, before)
		if err != nil {
			exportError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		templates.ExportList(statuses).Render(r.Context(), w)
	})
	r.Get("/exports/{export_id}/download", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		id, _ := strconv.ParseInt(chi.URLParam(r, "export_id"), 10, 64)
		file, err := svc.PrepareDownload(r.Context(), auth.Actor, id)
		if err != nil {
			exportError(w, r, err)
			return
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="csv-export-%d.csv"`, id))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Header.Del("Range")
		r.Header.Del("If-Modified-Since")
		r.Header.Del("If-None-Match")
		http.ServeContent(w, r, "export.csv", time.Time{}, file)
	})
	r.Get("/customers/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		p, err := auth.Service.Principal(r.Context(), auth.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		options := customerExportOptions(r)
		raw, _ := json.Marshal(options)
		if !p.Allows("customer.export", authorization.All) {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		if err := (exports.Customers{}).Authorize(p, raw); err != nil {
			authorizationError(w, r, err)
			return
		}
		templates.ExportReview(options, newOperationKey()).Render(r.Context(), w)
	})
	r.Post("/customers/export", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		raw, _ := json.Marshal(customerExportOptions(r))
		id, err := svc.Accept(r.Context(), auth.Actor, r.FormValue("operation_id"), (exports.Customers{}).Kind(), raw)
		if err != nil {
			exportError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), 303)
	})
	r.Get("/exports/{export_id}", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		id, _ := strconv.ParseInt(chi.URLParam(r, "export_id"), 10, 64)
		status, err := svc.Status(r.Context(), auth.Actor, id)
		if err != nil {
			exportError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Accept") == "application/json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(status)
			return
		}
		templates.ExportStatus(status).Render(r.Context(), w)
	})
}
func customerExportOptions(r *http.Request) exports.CustomerOptions {
	sort := r.FormValue("sort")
	if sort == "" {
		sort = "name"
	}
	return exports.CustomerOptions{Search: r.FormValue("search"), Sort: sort, Archived: r.FormValue("archived") == "1"}
}
func exportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, exports.ErrConflict) || errors.Is(err, exports.ErrNotReady) {
		http.Error(w, err.Error(), 409)
		return
	}
	if errors.Is(err, exports.ErrUnavailable) {
		slog.Warn("export download unavailable", "path", r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		templates.ExportUnavailable().Render(r.Context(), w)
		return
	}
	authorizationError(w, r, err)
}
