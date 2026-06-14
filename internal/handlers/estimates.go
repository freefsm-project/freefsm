package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type EstimateHandler struct {
	svc        *services.EstimateService
	custSvc    *services.CustomerService
	jobSvc     *services.JobService
	statusSvc  *services.StatusService
	itemSvc    *services.ItemService
	invoiceSvc *services.InvoiceService
}

func NewEstimateHandler(svc *services.EstimateService, custSvc *services.CustomerService, jobSvc *services.JobService, statusSvc *services.StatusService, itemSvc *services.ItemService, invoiceSvc *services.InvoiceService) *EstimateHandler {
	return &EstimateHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, statusSvc: statusSvc, itemSvc: itemSvc, invoiceSvc: invoiceSvc}
}

func (h *EstimateHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)

	estimates, total, err := h.svc.List(r.Context(), search, statusID, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.EstimateRow, len(estimates))
	for i, e := range estimates {
		rows[i] = estimateRow(e, statuses, custMap)
	}

	data := templates.EstimateListPageData{
		Estimates:  rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.EstimatePaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" {
		templates.EstimatesTable(data).Render(r.Context(), w)
		return
	}
	templates.EstimatesIndex(data).Render(r.Context(), w)
}

func (h *EstimateHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	e, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	d := estimateToDetail(e, statuses)
	d.LineItems = h.svc.LineItems(e)
	templates.EstimateShow(d).Render(r.Context(), w)
}

func (h *EstimateHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.EstimateForm(h.newEstimateForm(r.Context())).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	lineItems, _ := services.ParseLineItems(r.FormValue("line_items"))
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}

	params := services.EstimateCreateParams{
		CustomerID: custID,
		JobID:      jobID,
		StatusID:   statusID,
		Title:      r.FormValue("title"),
		Notes:      r.FormValue("notes"),
		TaxRate:    taxRate,
		LineItems:  lineItems,
	}
	if params.LineItems == nil {
		params.LineItems = []services.LineItem{}
	}
	_, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/estimates?flash=Estimate+created", http.StatusSeeOther)
}

func (h *EstimateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		e, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statuses := h.statusesForSelect(r.Context())
		fd := h.formDataFromEstimate(r.Context(), e, statuses)
		templates.EstimateForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	lineItems, _ := services.ParseLineItems(r.FormValue("line_items"))
	taxRate := r.FormValue("tax_rate")
	taxRatePtr := formPtr(taxRate)
	if taxRate == "" {
		t := "0"
		taxRatePtr = &t
	}

	params := services.EstimateUpdateParams{
		CustomerID: int64Ptr(custID),
		JobID:      int64Ptr(jobID),
		StatusID:   int64Ptr(statusID),
		Title:      formPtr(r.FormValue("title")),
		Notes:      formPtr(r.FormValue("notes")),
		TaxRate:    taxRatePtr,
	}
	if lineItems != nil {
		params.LineItems = &lineItems
	}
	if _, err := h.svc.Update(r.Context(), id, params); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+updated", id), http.StatusSeeOther)
}

func (h *EstimateHandler) ConvertToInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	inv, err := h.invoiceSvc.CreateFromEstimate(r.Context(), id, h.statusSvc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+created+from+estimate", inv.ID), http.StatusSeeOther)
}

func (h *EstimateHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	est, err := h.svc.CreateFromJob(r.Context(), id, h.statusSvc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+created+from+job", est.ID), http.StatusSeeOther)
}

func (h *EstimateHandler) PDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	e, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "estimate")
	var customer *ent.Customer
	if e.CustomerID != nil && *e.CustomerID > 0 {
		c, _ := h.custSvc.GetByID(r.Context(), *e.CustomerID)
		customer = c
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="EST-%05d.pdf"`, id))
	services.GenerateEstimatePDF(w, e, customer, statuses, middleware.CompanyFromContext(r.Context()))
}

func (h *EstimateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/estimates?flash=Estimate+deleted", http.StatusSeeOther)
}

func (h *EstimateHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "estimate")
	return statuses
}

func (h *EstimateHandler) itemsCatalog(ctx context.Context) string {
	items, _ := h.itemSvc.ListActive(ctx)
	return itemsToJSON(items)
}

func (h *EstimateHandler) existingItemsJSON(items []services.LineItem) string {
	return services.SerializeLineItems(items)
}

func (h *EstimateHandler) newEstimateForm(ctx context.Context) templates.EstimateFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	return templates.EstimateFormPageData{
		Estimate:          &templates.EstimateDetail{},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(ctx),
		ExistingItemsJSON: "[]",
	}
}

func (h *EstimateHandler) formDataFromEstimate(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) templates.EstimateFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	d := estimateToDetail(e, statuses)
	items := h.svc.LineItems(e)
	return templates.EstimateFormPageData{
		Estimate:          &d,
		IsNew:             false,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(ctx),
		ExistingItemsJSON: h.existingItemsJSON(items),
	}
}

func estimateToDetail(e *ent.Estimate, statuses []*ent.Status) templates.EstimateDetail {
	d := templates.EstimateDetail{
		ID:         e.ID,
		CustomerID: estCustID(e),
		StatusID:   estStatusID(e),
		StatusName:  statusName(statuses, e.StatusID),
		StatusColor: statusColor(statuses, e.StatusID),
		Title:      e.Title,
		Notes:      e.Notes,
		TaxRate:    e.TaxRate,
	}
	if e.JobID != nil {
		d.JobID = *e.JobID
	}
	return d
}

func estimateRow(e *ent.Estimate, statuses []*ent.Status, custMap map[int64]string) templates.EstimateRow {
	r := templates.EstimateRow{
		ID:         e.ID,
		Title:      e.Title,
		CustomerID: estCustID(e),
		Customer:   custMap[estCustID(e)],
		StatusID:   estStatusID(e),
		StatusName:  statusName(statuses, e.StatusID),
		StatusColor: statusColor(statuses, e.StatusID),
	}
	if !e.CreatedAt.IsZero() {
		r.CreatedAt = e.CreatedAt.Format("Jan 2, 2006")
	}
	return r
}

func estCustID(e *ent.Estimate) int64 {
	if e.CustomerID == nil {
		return 0
	}
	return *e.CustomerID
}

func estStatusID(e *ent.Estimate) int64 {
	if e.StatusID == nil {
		return 0
	}
	return *e.StatusID
}

func jobOptions(jobs []*ent.Job) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(jobs))
	for i, j := range jobs {
		label := j.JobType
		if j.Subtitle != "" {
			label += " — " + j.Subtitle
		}
		opts[i] = templates.SelectOption{Value: j.ID, Label: label}
	}
	return opts
}

type catalogItem struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	UnitPrice   float64 `json:"unit_price"`
	Taxable     bool    `json:"taxable"`
	TaxRate     string  `json:"tax_rate"`
}

func itemsToJSON(items []*ent.Item) string {
	if len(items) == 0 {
		return "[]"
	}
	c := make([]catalogItem, len(items))
	for i, item := range items {
		c[i] = catalogItem{
			ID:          item.ID,
			Name:        item.Name,
			Description: item.Description,
			UnitPrice:   item.UnitPrice,
			Taxable:     item.Taxable,
			TaxRate:     item.TaxRate,
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "[]"
	}
	return string(b)
}
