package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type ItemHandler struct{ svc *services.ItemService }

func NewItemHandler(svc *services.ItemService) *ItemHandler {
	return &ItemHandler{svc: svc}
}

func (h *ItemHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireItemPermission(w, r, "item.view") {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	search := r.URL.Query().Get("search")
	items, total, err := h.svc.List(r.Context(), search, page, 25)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	rows := make([]templates.ItemRow, len(items))
	for n, i := range items {
		rows[n] = itemRow(i)
	}
	d := templates.ItemListPageData{Items: rows, Page: page, PerPage: 25, Total: total, TotalPages: services.ItemPaginationTotalPages(total, 25), Search: search}
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.ItemsListContent(d).Render(r.Context(), w)
		return
	}
	templates.ItemsIndex(d).Render(r.Context(), w)
}

func (h *ItemHandler) Show(w http.ResponseWriter, r *http.Request) {
	if !requireItemPermission(w, r, "item.view") {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	i, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	templates.ItemShow(itemToDetail(r.Context(), i)).Render(r.Context(), w)
}

func (h *ItemHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireItemPermission(w, r, "item.create") {
		return
	}
	if r.Method == http.MethodGet {
		templates.ItemForm(newItemForm()).Render(r.Context(), w)
		return
	}
	h.save(w, r, 0, false)
}

func (h *ItemHandler) CreateInline(w http.ResponseWriter, r *http.Request) {
	if !requireItemPermission(w, r, "item.create") {
		return
	}
	if !requireItemPermission(w, r, "pricebook.create") {
		return
	}
	h.save(w, r, 0, true)
}

func (h *ItemHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !allRecordsPermission(r, "item.edit") && !allRecordsPermission(r, "pricebook.edit") && !allRecordsPermission(r, "cost.edit") {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		i, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		templates.ItemForm(formDataFromItem(r.Context(), i)).Render(r.Context(), w)
		return
	}
	h.save(w, r, id, false)
}

func (h *ItemHandler) save(w http.ResponseWriter, r *http.Request, id int64, inline bool) {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	text := func(key string) *string {
		if !r.PostForm.Has(key) {
			return nil
		}
		v := r.PostForm.Get(key)
		return &v
	}
	flag := func(key string) *bool {
		if !r.PostForm.Has(key) && !r.PostForm.Has(key+"_present") {
			return nil
		}
		v := r.PostForm.Get(key) == "true"
		return &v
	}
	in := authorization.ItemInput{Name: text("name"), Type: text("type"), Description: text("description"), Sku: text("sku"), TaxRate: text("tax_rate"), Taxable: flag("taxable"), TrackInventory: flag("track_inventory"), IsActive: flag("is_active")}
	for _, f := range []struct {
		key    string
		target **float64
	}{{"unit_price", &in.UnitPrice}, {"unit_cost", &in.UnitCost}} {
		if r.PostForm.Has(f.key) {
			v, err := strconv.ParseFloat(r.PostForm.Get(f.key), 64)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			*f.target = &v
		}
	}
	id, err := request.Service.SaveItem(r.Context(), request.Actor, id, in)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if inline {
		i, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(services.CatalogSnapshotFromItem(i))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/items/%d", id), 303)
}

func (h *ItemHandler) Delete(w http.ResponseWriter, r *http.Request)  { h.archive(w, r, true) }
func (h *ItemHandler) Restore(w http.ResponseWriter, r *http.Request) { h.archive(w, r, false) }
func (h *ItemHandler) archive(w http.ResponseWriter, r *http.Request, archived bool) {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := request.Service.SetItemArchived(r.Context(), request.Actor, id, archived); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/items", 303)
}

func requireItemPermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	if !allRecordsPermission(r, permission) {
		authorizationError(w, r, authorization.ErrForbidden)
		return false
	}
	return true
}

func parseFloat(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }
func floatPtr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool        { return &v }

func requireAdminOrDispatcher(w http.ResponseWriter, r *http.Request) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return false
	}
	if !isAdminOrDispatcher(u) {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}

func itemToDetail(ctx context.Context, i *ent.Item) templates.ItemDetail {
	d := templates.ItemDetail{ID: i.ID, Name: i.Name, Type: i.Type, Sku: i.Sku, UnitPrice: i.UnitPrice, UnitCost: i.UnitCost, Taxable: i.Taxable, TaxRate: i.TaxRate, TrackInventory: i.TrackInventory, Description: i.Description, IsActive: i.IsActive}
	if i.DeletedAt != nil && !i.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *i.DeletedAt)
	}
	return d
}
func itemRow(i *ent.Item) templates.ItemRow {
	return templates.ItemRow{ID: i.ID, Name: i.Name, Type: i.Type, Sku: i.Sku, UnitPrice: i.UnitPrice, UnitCost: i.UnitCost, IsActive: i.IsActive}
}
func newItemForm() templates.ItemFormPageData {
	return templates.ItemFormPageData{Item: &templates.ItemDetail{Type: "service", Taxable: true, IsActive: true}, IsNew: true, Types: services.ItemTypes}
}
func formDataFromItem(ctx context.Context, i *ent.Item) templates.ItemFormPageData {
	d := itemToDetail(ctx, i)
	return templates.ItemFormPageData{Item: &d, IsNew: false, Types: services.ItemTypes}
}
