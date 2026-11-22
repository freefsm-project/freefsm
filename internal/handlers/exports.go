package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func registerExports(r chi.Router, pool *pgxpool.Pool) {
	registerActivityExports(r, pool)
	registerTimeExports(r, pool)
	svc := exports.Product(pool)
	registerMasterExports(r, svc)
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

// These are office collection exports. Domain definitions, also registered in
// exports.Product, own selection and whole-artifact authority at every stage.
func registerMasterExports(r chi.Router, svc *exports.Service) {
	for _, route := range []struct {
		path, title, back, defaultSort string
		definition                     exports.Definition
		ids                            []string
	}{
		{"/contacts/export", "Contacts", "/contacts", "name", exports.Contact{}, []string{"customer_id"}},
		{"/locations/export", "Locations", "/locations", "name", exports.Location{}, []string{"customer_id"}},
		{"/projects/export", "Projects", "/projects", "name", exports.Project{}, []string{"status_id"}},
		{"/assets/export", "Assets", "/assets", "name", exports.Asset{}, []string{"customer_id", "asset_type_id", "asset_status_id"}},
		{"/service-entries/export", "Asset Service Entries", "/assets", "date", exports.ServiceEntry{}, []string{"asset_id"}},
		{"/items/export", "Item base", "/items", "name", exports.ItemBase{}, nil},
		{"/pricebook/export", "Pricebook", "/items", "name", exports.Pricebook{}, nil},
		{"/items/cost/export", "Item Cost", "/items", "name", exports.Cost{}, nil},
	} {
		handler := func(w http.ResponseWriter, r *http.Request) {
			auth, ok := authorization.FromContext(r.Context())
			if !ok {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			raw, fields, err := masterExportOptions(r, route.defaultSort, route.ids)
			if err == nil {
				err = route.definition.Validate(raw)
			}
			if err != nil {
				exportError(w, r, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodPost {
				id, err := svc.Accept(r.Context(), auth.Actor, r.FormValue("operation_id"), route.definition.Kind(), raw)
				if err != nil {
					exportError(w, r, err)
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/exports/%d", id), http.StatusSeeOther)
				return
			}
			p, err := auth.Service.Principal(r.Context(), auth.Actor)
			if err == nil {
				err = route.definition.Authorize(p, raw)
			}
			if err != nil {
				exportError(w, r, err)
				return
			}
			templates.MasterExportReview(route.title, route.path, route.back, newOperationKey(), fields, route.definition.Columns()).Render(r.Context(), w)
		}
		r.Get(route.path, handler)
		r.Post(route.path, handler)
	}
}

func masterExportOptions(r *http.Request, defaultSort string, ids []string) (json.RawMessage, url.Values, error) {
	if err := r.ParseForm(); err != nil {
		return nil, nil, exports.ErrConflict
	}
	allowed := map[string]bool{"search": true, "sort": true, "archived": true, "archived_only": true, "search_sku": true, "operation_id": true, "csrf_token": true}
	for _, key := range ids {
		allowed[key] = true
	}
	for key, values := range r.Form {
		if !allowed[key] || len(values) != 1 {
			return nil, nil, exports.ErrConflict
		}
	}
	sort := r.Form.Get("sort")
	if sort == "" {
		sort = defaultSort
	}
	archived := r.Form.Get("archived")
	if archived != "" && archived != "0" && archived != "1" {
		return nil, nil, exports.ErrConflict
	}
	options := map[string]any{"search": r.Form.Get("search"), "sort": sort, "archived": archived == "1"}
	fields := url.Values{"search": {r.Form.Get("search")}, "sort": {sort}, "archived": {"0"}}
	if archived == "1" {
		fields.Set("archived", "1")
	}
	if value := r.Form.Get("archived_only"); value != "" {
		if value != "0" && value != "1" || value == "1" && archived != "1" {
			return nil, nil, exports.ErrConflict
		}
		options["archived_only"] = value == "1"
		fields.Set("archived_only", value)
	}
	if value := r.Form.Get("search_sku"); value != "" {
		if value != "0" && value != "1" {
			return nil, nil, exports.ErrConflict
		}
		options["search_sku"] = value == "1"
		fields.Set("search_sku", value)
	}
	for _, key := range ids {
		if value := r.Form.Get(key); value != "" {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id < 0 {
				return nil, nil, exports.ErrConflict
			}
			options[key] = id
			fields.Set(key, strconv.FormatInt(id, 10))
		}
	}
	raw, err := json.Marshal(options)
	return raw, fields, err
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
