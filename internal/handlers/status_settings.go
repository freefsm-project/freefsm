package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type StatusSettingsHandler struct{ svc *statusflow.Service }

func NewStatusSettingsHandler(svc *statusflow.Service) *StatusSettingsHandler {
	return &StatusSettingsHandler{svc: svc}
}

func settingsStatusType(r *http.Request) (statusflow.ObjectType, bool) {
	typ := statusflow.ObjectType(chi.URLParam(r, "type"))
	return typ, typ == statusflow.Job || typ == statusflow.Project || typ == statusflow.Estimate || typ == statusflow.Invoice
}

func (h *StatusSettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	typ, ok := settingsStatusType(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Administrator authentication is required.", http.StatusForbidden)
		return
	}
	statuses, err := h.svc.Configuration(r.Context(), u.CompanyID, typ)
	if err != nil {
		statusflowHTTPError(w, err)
		return
	}
	data := templates.StatusSettingsPageData{ObjectType: string(typ)}
	for _, category := range statusflow.Categories {
		if category.ObjectType != typ {
			continue
		}
		column := templates.StatusCategoryColumn{Key: string(category.Key), Label: categoryLabel(category.Key), Automatic: !category.Manual}
		for _, item := range statuses {
			if item.Category == category.Key {
				column.Statuses = append(column.Statuses, templates.StatusSettingsRow{ID: item.ID, Name: item.Name, Color: item.Color, Order: item.Order, Default: item.Default, Usage: item.Usage})
			}
		}
		data.Categories = append(data.Categories, column)
	}
	if err := templates.StatusSettingsPage(data).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *StatusSettingsHandler) Create(w http.ResponseWriter, r *http.Request) {
	typ, ok := settingsStatusType(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The submitted status form is invalid.", 400)
		return
	}
	actor, ok := settingsActor(r)
	if !ok {
		http.Error(w, "Administrator authentication is required.", 403)
		return
	}
	_, err := h.svc.Create(r.Context(), actor, statusflow.CreateRequest{Type: typ, Name: r.FormValue("name"), Color: r.FormValue("color"), Category: statusflow.CategoryKey(r.FormValue("category"))})
	h.finish(w, r, typ, "Status created", err)
}

func (h *StatusSettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := statusRequest(r)
	if !ok {
		http.Error(w, "The status identifier or workflow is invalid.", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The submitted status form is invalid.", 400)
		return
	}
	actor, ok := settingsActor(r)
	if !ok {
		http.Error(w, "Administrator authentication is required.", 403)
		return
	}
	if !h.ownsStatus(r, actor.CompanyID, typ, id) {
		http.Error(w, "Status does not belong to this workflow.", 400)
		return
	}
	_, err := h.svc.Update(r.Context(), actor, id, r.FormValue("name"), r.FormValue("color"))
	h.finish(w, r, typ, "Status updated", err)
}

func (h *StatusSettingsHandler) Default(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := statusRequest(r)
	if !ok {
		http.Error(w, "Invalid status.", 400)
		return
	}
	actor, ok := settingsActor(r)
	if !ok {
		http.Error(w, "Administrator authentication is required.", 403)
		return
	}
	if !h.ownsStatus(r, actor.CompanyID, typ, id) {
		http.Error(w, "Status does not belong to this workflow.", 400)
		return
	}
	h.finish(w, r, typ, "Category default updated", h.svc.SetDefault(r.Context(), actor, id))
}

func (h *StatusSettingsHandler) Move(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := statusRequest(r)
	if !ok {
		http.Error(w, "Invalid status.", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The submitted move is invalid.", 400)
		return
	}
	actor, ok := settingsActor(r)
	if !ok {
		http.Error(w, "Administrator authentication is required.", 403)
		return
	}
	if !h.ownsStatus(r, actor.CompanyID, typ, id) {
		http.Error(w, "Status does not belong to this workflow.", 400)
		return
	}
	order := statusMoveOrder(r.Form)
	replacement, _ := strconv.ParseInt(r.FormValue("source_replacement_id"), 10, 64)
	err := h.svc.Move(r.Context(), actor, statusflow.MoveRequest{StatusID: id, Category: statusflow.CategoryKey(r.FormValue("category")), Order: order, SourceReplacementID: replacement, ConfirmInUse: r.FormValue("confirm_in_use") == "true"})
	h.finish(w, r, typ, "Status moved", err)
}

func statusMoveOrder(form url.Values) int {
	order, _ := strconv.Atoi(form.Get("order"))
	if requested, err := strconv.Atoi(form.Get("requested_order")); err == nil && requested > 0 {
		return requested
	}
	return order
}

func (h *StatusSettingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := statusRequest(r)
	if !ok {
		http.Error(w, "Invalid status.", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The submitted deletion is invalid.", 400)
		return
	}
	actor, ok := settingsActor(r)
	if !ok {
		http.Error(w, "Administrator authentication is required.", 403)
		return
	}
	if !h.ownsStatus(r, actor.CompanyID, typ, id) {
		http.Error(w, "Status does not belong to this workflow.", 400)
		return
	}
	replacement, _ := strconv.ParseInt(r.FormValue("replacement_status_id"), 10, 64)
	h.finish(w, r, typ, "Status deleted", h.svc.Delete(r.Context(), actor, id, replacement))
}

func statusRequest(r *http.Request) (statusflow.ObjectType, int64, bool) {
	typ, ok := settingsStatusType(r)
	if !ok {
		return "", 0, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return typ, id, err == nil && id > 0
}
func settingsActor(r *http.Request) (statusflow.Actor, bool) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		return statusflow.Actor{}, false
	}
	return statusflow.Actor{ID: u.ID, CompanyID: u.CompanyID}, true
}
func (h *StatusSettingsHandler) ownsStatus(r *http.Request, companyID int64, typ statusflow.ObjectType, id int64) bool {
	items, err := h.svc.Configuration(r.Context(), companyID, typ)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func (h *StatusSettingsHandler) finish(w http.ResponseWriter, r *http.Request, typ statusflow.ObjectType, message string, err error) {
	if err != nil {
		statusflowHTTPError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/settings/statuses/%s?flash=%s", typ, url.QueryEscape(message)), http.StatusSeeOther)
}
func categoryLabel(key statusflow.CategoryKey) string {
	labels := map[statusflow.CategoryKey]string{
		statusflow.JobNew: "New", statusflow.JobTravelTime: "Travel Time", statusflow.JobInProgress: "In Progress", statusflow.JobPending: "Pending", statusflow.JobCompleted: "Completed", statusflow.JobCanceled: "Canceled",
		statusflow.ProjectNew: "Opportunity", statusflow.ProjectInProgress: "In Progress", statusflow.ProjectPending: "Pending", statusflow.ProjectCompleted: "Completed", statusflow.ProjectCanceled: "Canceled",
		statusflow.EstimateDraft: "Draft", statusflow.EstimateEstimate: "Estimate", statusflow.EstimateSent: "Sent", statusflow.EstimateAccepted: "Accepted", statusflow.EstimateRejected: "Rejected", statusflow.EstimateCompleted: "Completed",
		statusflow.InvoiceDraft: "Draft", statusflow.InvoiceInvoiced: "Invoiced", statusflow.InvoiceSent: "Sent", statusflow.InvoicePartiallyPaid: "Partially Paid", statusflow.InvoicePaid: "Paid", statusflow.InvoiceVoid: "Void",
	}
	return labels[key]
}
