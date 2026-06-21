package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type ItemHandler struct {
	svc         *services.ItemService
	activitySvc *services.ActivityService
}

func NewItemHandler(svc *services.ItemService, activitySvc *services.ActivityService) *ItemHandler {
	return &ItemHandler{svc: svc, activitySvc: activitySvc}
}

func (h *ItemHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")

	items, total, err := h.svc.List(r.Context(), search, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	rows := make([]templates.ItemRow, len(items))
	for i, item := range items {
		rows[i] = itemRow(item)
	}

	data := templates.ItemListPageData{
		Items:      rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.ItemPaginationTotalPages(total, perPage),
		Search:     search,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.ItemsTable(data).Render(r.Context(), w)
		return
	}
	templates.ItemsIndex(data).Render(r.Context(), w)
}

func (h *ItemHandler) Show(w http.ResponseWriter, r *http.Request) {
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
	templates.ItemShow(itemToDetail(i)).Render(r.Context(), w)
}

func (h *ItemHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.ItemForm(newItemForm()).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	params := services.ItemCreateParams{
		Name:           r.FormValue("name"),
		Type:           r.FormValue("type"),
		Sku:            r.FormValue("sku"),
		UnitPrice:      parseFloat(r.FormValue("unit_price")),
		UnitCost:       parseFloat(r.FormValue("unit_cost")),
		Taxable:        r.FormValue("taxable") == "true",
		TaxRate:        r.FormValue("tax_rate"),
		TrackInventory: r.FormValue("track_inventory") == "true",
		Description:    r.FormValue("description"),
		IsActive:       r.FormValue("is_active") == "true",
	}
	if params.Type == "" {
		params.Type = "service"
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "item", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/items?flash=Item+created", http.StatusSeeOther)
}

func (h *ItemHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		templates.ItemForm(formDataFromItem(i)).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	params := services.ItemUpdateParams{
		Name:           formPtr(r.FormValue("name")),
		Type:           formPtr(r.FormValue("type")),
		Sku:            formPtr(r.FormValue("sku")),
		UnitPrice:      floatPtr(parseFloat(r.FormValue("unit_price"))),
		UnitCost:       floatPtr(parseFloat(r.FormValue("unit_cost"))),
		Taxable:        boolPtr(r.FormValue("taxable") == "true"),
		TaxRate:        formPtr(r.FormValue("tax_rate")),
		TrackInventory: boolPtr(r.FormValue("track_inventory") == "true"),
		Description:    formPtr(r.FormValue("description")),
		IsActive:       boolPtr(r.FormValue("is_active") == "true"),
	}
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "item", id, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/items/%d?flash=Item+updated", id), http.StatusSeeOther)
}

func (h *ItemHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	item, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := item.Name
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "item", id, map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/items?flash=Item+archived", http.StatusSeeOther)
}

func (h *ItemHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	i, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.svc.Restore(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "restored", "item", id, map[string]interface{}{
			"entity_name": i.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10)+"?flash=Item+restored", http.StatusSeeOther)
}

func parseFloat(v string) float64 {
	f, _ := strconv.ParseFloat(v, 64)
	return f
}

func floatPtr(v float64) *float64 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func itemToDetail(i *ent.Item) templates.ItemDetail {
	d := templates.ItemDetail{
		ID:             i.ID,
		Name:           i.Name,
		Type:           i.Type,
		Sku:            i.Sku,
		UnitPrice:      i.UnitPrice,
		UnitCost:       i.UnitCost,
		Taxable:        i.Taxable,
		TaxRate:        i.TaxRate,
		TrackInventory: i.TrackInventory,
		Description:    i.Description,
		IsActive:       i.IsActive,
	}
	if i.DeletedAt != nil && !i.DeletedAt.IsZero() {
		d.ArchivedAt = i.DeletedAt.Format("Jan 2, 2006")
	}
	return d
}

func itemRow(i *ent.Item) templates.ItemRow {
	return templates.ItemRow{
		ID:        i.ID,
		Name:      i.Name,
		Type:      i.Type,
		Sku:       i.Sku,
		UnitPrice: i.UnitPrice,
		UnitCost:  i.UnitCost,
		IsActive:  i.IsActive,
	}
}

func newItemForm() templates.ItemFormPageData {
	return templates.ItemFormPageData{
		Item: &templates.ItemDetail{
			Type:     "service",
			Taxable:  true,
			IsActive: true,
		},
		IsNew: true,
		Types: services.ItemTypes,
	}
}

func formDataFromItem(i *ent.Item) templates.ItemFormPageData {
	d := itemToDetail(i)
	return templates.ItemFormPageData{
		Item:  &d,
		IsNew: false,
		Types: services.ItemTypes,
	}
}
