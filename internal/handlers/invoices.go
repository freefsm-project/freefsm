package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type InvoiceHandler struct {
	svc         *services.InvoiceService
	custSvc     *services.CustomerService
	jobSvc      *services.JobService
	statusSvc   *services.StatusService
	itemSvc     *services.ItemService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	fileSvc     *services.FileService
	activitySvc *services.ActivityService
}

func NewInvoiceHandler(svc *services.InvoiceService, custSvc *services.CustomerService, jobSvc *services.JobService, statusSvc *services.StatusService, itemSvc *services.ItemService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, activitySvc *services.ActivityService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, statusSvc: statusSvc, itemSvc: itemSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, activitySvc: activitySvc}
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

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
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
	d := invoiceToDetail(i, statuses)
	if i.CustomerID != nil && *i.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), *i.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(i)
	d.Payments = h.svc.Payments(i)
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "invoice", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	d.CustomFields = buildCustomFieldDisplay(defs, i.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), "invoice", id)
	d.FileList = templates.FileListPageData{Files: filesToRows(files), ObjectID: id, ObjectType: "invoice"}
	ctx := middleware.WithPageHeaderTitle(r.Context(), i.Title)
	templates.InvoiceShow(d).Render(ctx, w)
}

func (h *InvoiceHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, "invoice", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", "invoice", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "invoice", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/invoices/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *InvoiceHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, "invoice", id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", "invoice", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "invoice", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/invoices/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
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
	lineItems, _ := services.ParseLineItems(r.FormValue("line_items"))
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}

	loc := middleware.CompanyLocation(r.Context())
	params := services.InvoiceCreateParams{
		CustomerID:  custID,
		JobID:       jobID,
		StatusID:    statusID,
		Title:       r.FormValue("title"),
		Notes:       r.FormValue("notes"),
		InvoiceDate: parseDate(r.FormValue("invoice_date"), loc),
		DueDate:     parseDate(r.FormValue("due_date"), loc),
		TaxRate:     taxRate,
		LineItems:    lineItems,
		CustomFields: parseCustomFieldValues(r),
	}
	if params.LineItems == nil {
		params.LineItems = []services.LineItem{}
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "invoice", result.ID, map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
		})
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
	lineItems, _ := services.ParseLineItems(r.FormValue("line_items"))
	taxRate := r.FormValue("tax_rate")
	taxRatePtr := formPtr(taxRate)
	if taxRate == "" {
		t := "0"
		taxRatePtr = &t
	}

	params := services.InvoiceUpdateParams{
		CustomerID: int64Ptr(custID),
		JobID:      int64Ptr(jobID),
		StatusID:   int64Ptr(statusID),
		Title:      formPtr(r.FormValue("title")),
		Notes:      formPtr(r.FormValue("notes")),
		TaxRate:    taxRatePtr,
	}
	loc := middleware.CompanyLocation(r.Context())
	if d := r.FormValue("invoice_date"); d != "" {
		t := parseDate(d, loc)
		params.InvoiceDate = &t
	}
	if d := r.FormValue("due_date"); d != "" {
		t := parseDate(d, loc)
		params.DueDate = &t
	}
	if lineItems != nil {
		params.LineItems = &lineItems
	}
	params.CustomFields = strPtr(parseCustomFieldValues(r))
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "invoice", id, map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+updated", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	inv, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := inv.Title
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "invoice", id, map[string]interface{}{
			"entity_name": title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/invoices?flash=Invoice+archived", http.StatusSeeOther)
}

func (h *InvoiceHandler) Restore(w http.ResponseWriter, r *http.Request) {
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
		h.activitySvc.Record(r.Context(), u.ID, "restored", "invoice", id, map[string]interface{}{
			"entity_name": i.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/invoices/"+strconv.FormatInt(id, 10)+"?flash=Invoice+restored", http.StatusSeeOther)
}

func (h *InvoiceHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	return statuses
}

func (h *InvoiceHandler) itemsCatalog(ctx context.Context) string {
	items, _ := h.itemSvc.ListActive(ctx)
	return itemsToJSON(items)
}

func (h *InvoiceHandler) newInvoiceForm(ctx context.Context) templates.InvoiceFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	return templates.InvoiceFormPageData{
		Invoice:           &templates.InvoiceDetail{},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(ctx),
		ExistingItemsJSON: "[]",
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
	}
}

func (h *InvoiceHandler) formDataFromInvoice(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) templates.InvoiceFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	d := invoiceToDetail(i, statuses)
	items := h.svc.LineItems(i)
	return templates.InvoiceFormPageData{
		Invoice:           &d,
		IsNew:             false,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(ctx),
		ExistingItemsJSON: services.SerializeLineItems(items),
		CustomFields:      buildCustomFieldDisplay(defs, i.CustomFields),
	}
}

func invoiceToDetail(i *ent.Invoice, statuses []*ent.Status) templates.InvoiceDetail {
	d := templates.InvoiceDetail{
		ID:         i.ID,
		CustomerID: invCustID(i),
		StatusID:   invStatusID(i),
		StatusName:  statusName(statuses, i.StatusID),
		StatusColor: statusColor(statuses, i.StatusID),
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
	if i.DeletedAt != nil && !i.DeletedAt.IsZero() {
		d.ArchivedAt = i.DeletedAt.Format("Jan 2, 2006")
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
		StatusName:  statusName(statuses, i.StatusID),
		StatusColor: statusColor(statuses, i.StatusID),
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

func (h *InvoiceHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	inv, err := h.svc.CreateFromJob(r.Context(), id, h.statusSvc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "invoice", inv.ID, map[string]interface{}{
			"entity_name": inv.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+created+from+job", inv.ID), http.StatusSeeOther)
}

func (h *InvoiceHandler) CreateFromEstimate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	inv, err := h.svc.CreateFromEstimate(r.Context(), id, h.statusSvc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "invoice", inv.ID, map[string]interface{}{
			"entity_name": inv.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+created+from+estimate", inv.ID), http.StatusSeeOther)
}

func (h *InvoiceHandler) PDF(w http.ResponseWriter, r *http.Request) {
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
	statuses, _ := h.statusSvc.ByObjectType(r.Context(), "invoice")
	var customer *ent.Customer
	if i.CustomerID != nil && *i.CustomerID > 0 {
		c, _ := h.custSvc.GetByID(r.Context(), *i.CustomerID)
		customer = c
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="INV-%05d.pdf"`, id))
	services.GenerateInvoicePDF(w, i, customer, statuses, middleware.CompanyFromContext(r.Context()))
}

func (h *InvoiceHandler) RecordPayment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	amount, _ := strconv.ParseFloat(r.FormValue("amount"), 64)
	payment := services.Payment{
		Amount:    amount,
		Method:    r.FormValue("method"),
		Reference: r.FormValue("reference"),
		Date:      r.FormValue("date"),
		Notes:     r.FormValue("notes"),
	}
	if err := h.svc.RecordPayment(r.Context(), id, payment); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "payment_recorded", "invoice", id, map[string]interface{}{
			"actor_name": u.Name,
			"amount":     fmt.Sprintf("%.2f", amount),
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+recorded", id), http.StatusSeeOther)
}
