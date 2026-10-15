package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func customerChildren(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		var customerID int64
		if raw := chi.URLParam(r, "id"); raw != "" {
			var err error
			customerID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || customerID <= 0 {
				http.NotFound(w, r)
				return
			}
		}
		var id int64
		if raw := chi.URLParam(r, "childID"); raw != "" {
			var err error
			id, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.NotFound(w, r)
				return
			}
		}
		base := fmt.Sprintf("/customers/%d/%ss", customerID, kind)
		p, err := req.Service.Principal(r.Context(), req.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		if customerID == 0 || !p.Allows("customer.view", authorization.All) {
			base = "/" + kind + "s"
		}
		if r.Method == http.MethodPost {
			if err = r.ParseForm(); err != nil {
				http.Error(w, "Invalid form", 400)
				return
			}
			if r.Form.Has("customer_id") && r.Form.Get("customer_id") != strconv.FormatInt(customerID, 10) {
				authorizationError(w, r, authorization.ErrConflict)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/delete") || strings.HasSuffix(r.URL.Path, "/restore") {
				err = req.Service.CustomerChildLifecycle(r.Context(), req.Actor, customerID, id, kind, strings.HasSuffix(r.URL.Path, "/restore"))
			} else {
				fields := map[string]string{}
				for _, key := range []string{"first_name", "last_name", "email", "phone", "role_title", "title", "address_1", "address_2", "city", "state", "zip_code", "notes", "service_notes", "access_notes"} {
					if r.Form.Has(key) {
						fields[key] = r.Form.Get(key)
					}
				}
				var primary *bool
				if r.Form.Has("is_primary") || r.Form.Has("primary_present") {
					v := r.Form.Get("is_primary") == "true" || r.Form.Get("is_primary") == "on"
					primary = &v
				}
				id, err = req.Service.SaveCustomerChild(r.Context(), req.Actor, customerID, id, kind, fields, primary)
			}
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/inline") {
				c, err := req.Service.CustomerChild(r.Context(), req.Actor, customerID, id, kind)
				if err != nil {
					authorizationError(w, r, err)
					return
				}
				writeInlineOptionJSON(w, id, c.Name())
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/%ss/%d", kind, id), 303)
			return
		}
		archived := r.URL.Query().Get("archived") == "1"
		optionsOnly := strings.HasSuffix(r.URL.Path, "/options")
		if optionsOnly {
			archived = false
		}
		if id != 0 && p.Allows(kind+".archived.view", authorization.All) {
			archived = true
		}
		var rows []authorization.CustomerChild
		if id != 0 {
			var c authorization.CustomerChild
			c, err = req.Service.CustomerChild(r.Context(), req.Actor, customerID, id, kind)
			rows = []authorization.CustomerChild{c}
		} else {
			rows, err = req.Service.CustomerChildren(r.Context(), req.Actor, customerID, kind, archived)
		}
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		data := templates.CustomerChildrenData{Kind: kind, CustomerID: customerID, BaseURL: base, Rows: rows, Archived: archived, CanCreate: p.Allows(kind+".create", authorization.All), CanEdit: p.Allows(kind+".edit", authorization.All), CanArchive: p.Allows(kind+".archive", authorization.All), CanRestore: p.Allows(kind+".restore", authorization.All), CanViewArchived: p.Allows(kind+".archived.view", authorization.All)}
		data.CanViewCustomer = p.Allows("customer.view", authorization.All) && req.Service.CustomerAccess(r.Context(), req.Actor, customerID, "customer.view") == nil
		if !data.CanViewCustomer {
			data.BaseURL = "/" + kind + "s"
		}
		data.CanCreate = data.CanCreate && data.CanViewCustomer
		if customerID == 0 {
			data.CanCreate = false
		}
		if strings.HasSuffix(r.URL.Path, "/new") {
			active, err := req.Service.CustomerActive(r.Context(), req.Actor, customerID)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			if !active {
				authorizationError(w, r, authorization.ErrForbidden)
				return
			}
			if !data.CanCreate {
				authorizationError(w, r, authorization.ErrForbidden)
				return
			}
			data.Editing = true
			data.Selected = &authorization.CustomerChild{CustomerID: customerID}
		}
		if id != 0 {
			for i := range rows {
				if rows[i].ID == id {
					data.Selected = &rows[i]
					break
				}
			}
			if data.Selected == nil {
				http.NotFound(w, r)
				return
			}
			data.CustomerID = data.Selected.CustomerID
			data.CanViewCustomer = data.CustomerID != 0
			if !data.Selected.CanMutate {
				data.CanEdit = false
				data.CanArchive = false
				data.CanRestore = false
			}
			if strings.HasSuffix(r.URL.Path, "/edit") {
				if !data.CanEdit || data.Selected.DeletedAt != nil {
					authorizationError(w, r, authorization.ErrForbidden)
					return
				}
				data.Editing = true
			}
		}
		if data.Selected == nil && !strings.HasSuffix(r.URL.Path, "/options") {
			data.Search = r.URL.Query().Get("search")
			filtered := []authorization.CustomerChild{}
			for _, c := range rows {
				if data.Search == "" || strings.Contains(strings.ToLower(strings.Join([]string{c.Name(), c.Email, c.Phone, c.Address1, c.Address2, c.City, c.State, c.ZipCode}, " ")), strings.ToLower(data.Search)) {
					filtered = append(filtered, c)
				}
			}
			data.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
			if data.Page < 1 {
				data.Page = 1
			}
			data.PerPage, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
			if data.PerPage != 50 && data.PerPage != 100 {
				data.PerPage = 25
			}
			data.Total = len(filtered)
			start := min((data.Page-1)*data.PerPage, data.Total)
			rows = filtered[start:min(start+data.PerPage, data.Total)]
			data.Rows = rows
		}
		if optionsOnly {
			active := rows[:0]
			for _, c := range rows {
				if c.CanMutate {
					active = append(active, c)
				}
			}
			rows = active
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			if data.Selected != nil {
				json.NewEncoder(w).Encode(data.Selected)
			} else {
				json.NewEncoder(w).Encode(rows)
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/options") {
			opts := []templates.SelectOption{}
			for _, c := range rows {
				opts = append(opts, templates.SelectOption{Value: c.ID, Label: c.Name()})
			}
			selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
			if kind == "location" {
				templates.LocationOptions(opts, selected).Render(r.Context(), w)
			} else {
				templates.ContactOptions(opts, selected).Render(r.Context(), w)
			}
			return
		}
		templates.CustomerChildrenPage(data).Render(r.Context(), w)
	}
}

func registerCustomerChildren(r chi.Router) {
	r.Get("/customers/{id}/archive", customerArchivePreview)
	r.Get("/customers/{id}/work", customerRelatedWork)
	for _, kind := range []string{"contact", "location"} {
		h := customerChildren(kind)
		for _, suffix := range []string{"", "/options", "/{childID}", "/{childID}/edit"} {
			r.Get("/"+kind+"s"+suffix, h)
		}
		for _, suffix := range []string{"/{childID}", "/{childID}/delete", "/{childID}/restore"} {
			r.Post("/"+kind+"s"+suffix, h)
		}
		base := "/customers/{id}/" + kind + "s"
		for _, suffix := range []string{"", "/new", "/options", "/{childID}", "/{childID}/edit"} {
			r.Get(base+suffix, h)
		}
		for _, suffix := range []string{"", "/inline", "/{childID}", "/{childID}/delete", "/{childID}/restore"} {
			r.Post(base+suffix, h)
		}
	}
}

func customerArchivePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", 403)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	p, err := req.Service.PreviewCustomerArchive(r.Context(), req.Actor, id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
		return
	}
	templates.CustomerArchiveReview(p).Render(r.Context(), w)
}

func customerRelatedWork(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", 403)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	kind := r.URL.Query().Get("type")
	switch kind {
	case "", "project", "job", "estimate", "invoice":
	default:
		http.Error(w, "Invalid work type", http.StatusBadRequest)
		return
	}
	archived := r.URL.Query().Get("archived") == "1"
	rows, err := req.Service.CustomerWorkCollection(r.Context(), req.Actor, id, kind, archived)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	filtered := []authorization.CustomerWork{}
	for _, row := range rows {
		if kind == "" || row.Kind == kind {
			filtered = append(filtered, row)
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if size != 50 && size != 100 {
		size = 25
	}
	total := len(filtered)
	start := min((page-1)*size, total)
	filtered = filtered[start:min(start+size, total)]
	templates.CustomerWorkPage(id, filtered, kind, page, size, total, archived).Render(r.Context(), w)
}
