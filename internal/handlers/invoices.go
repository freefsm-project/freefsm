package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type InvoiceHandler struct {
	svc       *services.InvoiceService
	custSvc   *services.CustomerService
	jobSvc    *services.JobService
	statusSvc *services.StatusService
}

func NewInvoiceHandler(svc *services.InvoiceService, custSvc *services.CustomerService, jobSvc *services.JobService, statusSvc *services.StatusService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, statusSvc: statusSvc}
}

func (h *InvoiceHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)

	invoices, total, err := h.svc.List(r.Context(), search, statusID, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.InvoiceRow, len(invoices))
	for i, inv := range invoices {
		rows[i] = invoiceRow(inv, statuses, custMap)
	}

	data := templates.InvoiceListPageData{
		Invoices:   rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.InvoicePaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" {
		templates.InvoicesTable(data).Render(r.Context(), w)
		return
	}
	templates.InvoicesIndex(data).Render(r.Context(), w)
}

func (h *InvoiceHandler) Show(w http.ResponseWriter, r *http.Request) {
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
	statuses := h.statusesForSelect(r.Context())
	templates.InvoiceShow(invoiceToDetail(i, statuses)).Render(r.Context(), w)
}

func (h *InvoiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.InvoiceForm(h.newInvoiceForm(r.Context())).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	params := services.InvoiceCreateParams{
		CustomerID:  custID,
		JobID:       jobID,
		StatusID:    statusID,
		Title:       r.FormValue("title"),
		Notes:       r.FormValue("notes"),
		InvoiceDate: parseDate(r.FormValue("invoice_date")),
		DueDate:     parseDate(r.FormValue("due_date")),
		TaxRate:     r.FormValue("tax_rate"),
	}
	_, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/invoices?flash=Invoice+created", http.StatusSeeOther)
}

func (h *InvoiceHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		statuses := h.statusesForSelect(r.Context())
		fd := h.formDataFromInvoice(r.Context(), i, statuses)
		templates.InvoiceForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	params := services.InvoiceUpdateParams{
		CustomerID: int64Ptr(custID),
		JobID:      int64Ptr(jobID),
		StatusID:   int64Ptr(statusID),
		Title:      formPtr(r.FormValue("title")),
		Notes:      formPtr(r.FormValue("notes")),
		TaxRate:    formPtr(r.FormValue("tax_rate")),
	}
	if d := r.FormValue("invoice_date"); d != "" {
		t := parseDate(d)
		params.InvoiceDate = &t
	}
	if d := r.FormValue("due_date"); d != "" {
		t := parseDate(d)
		params.DueDate = &t
	}
	if _, err := h.svc.Update(r.Context(), id, params); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+updated", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/invoices?flash=Invoice+deleted", http.StatusSeeOther)
}

func (h *InvoiceHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	return statuses
}

func (h *InvoiceHandler) newInvoiceForm(ctx context.Context) templates.InvoiceFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	return templates.InvoiceFormPageData{
		Invoice:   &templates.InvoiceDetail{},
		IsNew:     true,
		Customers: customerOptions(customers),
		Jobs:      jobOptions(jobs),
		Statuses:  statusOptions(statuses),
	}
}

func (h *InvoiceHandler) formDataFromInvoice(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) templates.InvoiceFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	d := invoiceToDetail(i, statuses)
	return templates.InvoiceFormPageData{
		Invoice:   &d,
		IsNew:     false,
		Customers: customerOptions(customers),
		Jobs:      jobOptions(jobs),
		Statuses:  statusOptions(statuses),
	}
}

func invoiceToDetail(i *ent.Invoice, statuses []*ent.Status) templates.InvoiceDetail {
	d := templates.InvoiceDetail{
		ID:         i.ID,
		CustomerID: invCustID(i),
		StatusID:   invStatusID(i),
		StatusName: statusName(statuses, i.StatusID),
		Title:      i.Title,
		Notes:      i.Notes,
		TaxRate:    i.TaxRate,
	}
	if i.JobID != nil {
		d.JobID = *i.JobID
	}
	if !i.InvoiceDate.IsZero() {
		d.InvoiceDate = i.InvoiceDate.Format("2006-01-02")
	}
	if !i.DueDate.IsZero() {
		d.DueDate = i.DueDate.Format("2006-01-02")
	}
	return d
}

func invoiceRow(i *ent.Invoice, statuses []*ent.Status, custMap map[int64]string) templates.InvoiceRow {
	r := templates.InvoiceRow{
		ID:         i.ID,
		Title:      i.Title,
		CustomerID: invCustID(i),
		Customer:   custMap[invCustID(i)],
		StatusID:   invStatusID(i),
		StatusName: statusName(statuses, i.StatusID),
	}
	if !i.InvoiceDate.IsZero() {
		r.InvoiceDate = i.InvoiceDate.Format("Jan 2, 2006")
	}
	if !i.DueDate.IsZero() {
		r.DueDate = i.DueDate.Format("Jan 2, 2006")
	}
	return r
}

func invCustID(i *ent.Invoice) int64 {
	if i.CustomerID == nil {
		return 0
	}
	return *i.CustomerID
}

func invStatusID(i *ent.Invoice) int64 {
	if i.StatusID == nil {
		return 0
	}
	return *i.StatusID
}
