package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	assetSvc    *services.AssetService
	statusSvc   *services.StatusService
	itemSvc     *services.ItemService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	fileSvc     *services.FileService
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
}

func NewInvoiceHandler(svc *services.InvoiceService, custSvc *services.CustomerService, jobSvc *services.JobService, assetSvc *services.AssetService, statusSvc *services.StatusService, itemSvc *services.ItemService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, emailSvc *services.EmailService, activitySvc *services.ActivityService, policySvc *services.PolicyService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, assetSvc: assetSvc, statusSvc: statusSvc, itemSvc: itemSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, emailSvc: emailSvc, activitySvc: activitySvc, policySvc: policySvc}
}

func (h *InvoiceHandler) authorizeInvoice(w http.ResponseWriter, r *http.Request, id int64, action string) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "invoice", id, action) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (h *InvoiceHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)

	var invoices []*ent.Invoice
	var total int
	var err error
	if customerID > 0 {
		invoices, total, err = h.svc.ListForCustomer(r.Context(), customerID, search, statusID, page, perPage)
	} else {
		invoices, total, err = h.svc.List(r.Context(), search, statusID, page, perPage)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.InvoiceRow, len(invoices))
	for i, inv := range invoices {
		rows[i] = invoiceRow(r.Context(), inv, statuses, custMap)
	}

	data := templates.InvoiceListPageData{
		Invoices:   rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.InvoicePaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		CustomerID: customerID,
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
	if !h.authorizeInvoice(w, r, id, policyRead) {
		return
	}
	i, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	d := invoiceToDetail(r.Context(), i, statuses)
	if i.CustomerID != nil && *i.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), *i.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	if i.JobID != nil && *i.JobID > 0 {
		job, _ := h.jobSvc.GetByID(r.Context(), *i.JobID)
		if job != nil && job.AssetID != nil && *job.AssetID > 0 {
			asset, _ := h.assetSvc.GetByID(r.Context(), *job.AssetID)
			if asset != nil {
				d.AssetID = asset.ID
				d.AssetName = assetLabel(asset)
			}
		}
	}
	d.LineItems = h.svc.LineItems(i)
	d.Payments = displayPayments(r.Context(), h.svc.Payments(i))
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "invoice", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	d.CustomFields = buildCustomFieldDisplay(defs, i.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), "invoice", id)
	d.FileList = templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "invoice"}
	ctx := middleware.WithPageHeaderTitle(r.Context(), i.Title)
	templates.InvoiceShow(d).Render(ctx, w)
}

func (h *InvoiceHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, "invoice", id)
	if err != nil {
		internalServerError(w, r, "attach invoice tag", err)
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
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, "invoice", id); err != nil {
		internalServerError(w, r, "detach invoice tag", err)
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
	invoiceNumber, err := parseOptionalPositiveInt64(r.FormValue("invoice_number"), "invoice number")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}

	loc := middleware.CompanyLocation(r.Context())
	params := services.InvoiceCreateParams{
		InvoiceNumber: invoiceNumber,
		CustomerID:    custID,
		JobID:         jobID,
		StatusID:      statusID,
		Title:         r.FormValue("title"),
		Notes:         r.FormValue("notes"),
		InvoiceDate:   parseDate(r.FormValue("invoice_date"), loc),
		DueDate:       parseDate(r.FormValue("due_date"), loc),
		TaxRate:       taxRate,
		LineItems:     lineItems,
		CustomFields:  parseCustomFieldValues(r),
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
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+created", result.ID), http.StatusSeeOther)
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
	invoiceNumber, err := parseRequiredPositiveInt64(r.FormValue("invoice_number"), "invoice number")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	taxRate := r.FormValue("tax_rate")
	taxRatePtr := formPtr(taxRate)
	if taxRate == "" {
		t := "0"
		taxRatePtr = &t
	}

	params := services.InvoiceUpdateParams{
		InvoiceNumber: invoiceNumber,
		CustomerID:    int64Ptr(custID),
		JobID:         &jobID,
		StatusID:      int64Ptr(statusID),
		Title:         formPtr(r.FormValue("title")),
		Notes:         formPtr(r.FormValue("notes")),
		TaxRate:       taxRatePtr,
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

func (h *InvoiceHandler) Finalize(w http.ResponseWriter, r *http.Request) {
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
	status, err := h.statusSvc.FindByName(r.Context(), "invoice", "Invoiced")
	if err != nil || status == nil {
		http.Error(w, "invoice status 'Invoiced' not found", 500)
		return
	}
	if !h.invoiceHasStatus(r.Context(), i, "Draft") {
		http.Error(w, "only draft invoices can be finalized", http.StatusConflict)
		return
	}
	defaultDueDays := 30
	if cs := middleware.CompanyFromContext(r.Context()); cs != nil {
		defaultDueDays = cs.DefaultDueDays
	}
	now := time.Now().In(middleware.CompanyLocation(r.Context()))
	invoiceDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	result, err := h.svc.Finalize(r.Context(), id, status.ID, invoiceDate, defaultDueDays)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "finalized", "invoice", id, map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
			"old_status":  statusName(h.statusesForSelect(r.Context()), i.StatusID),
			"new_status":  status.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+finalized", id), http.StatusSeeOther)
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
	d := invoiceToDetail(ctx, i, statuses)
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

func invoiceToDetail(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) templates.InvoiceDetail {
	statusName := statusName(statuses, i.StatusID)
	d := templates.InvoiceDetail{
		ID:          i.ID,
		Number:      i.InvoiceNumber,
		CustomerID:  invCustID(i),
		StatusID:    invStatusID(i),
		StatusName:  statusName,
		StatusColor: statusColor(statuses, i.StatusID),
		CanFinalize: strings.EqualFold(statusName, "Draft"),
		Title:       i.Title,
		Notes:       i.Notes,
		TaxRate:     i.TaxRate,
	}
	if i.JobID != nil {
		d.JobID = *i.JobID
	}
	if !i.InvoiceDate.IsZero() {
		d.InvoiceDate = i.InvoiceDate.Format("2006-01-02")
		d.InvoiceDateDisplay = displayDate(ctx, i.InvoiceDate)
	}
	if !i.DueDate.IsZero() {
		d.DueDate = i.DueDate.Format("2006-01-02")
		d.DueDateDisplay = displayDate(ctx, i.DueDate)
	}
	if i.DeletedAt != nil && !i.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *i.DeletedAt)
	}
	return d
}

func invoiceRow(ctx context.Context, i *ent.Invoice, statuses []*ent.Status, custMap map[int64]string) templates.InvoiceRow {
	r := templates.InvoiceRow{
		ID:          i.ID,
		Number:      i.InvoiceNumber,
		Title:       i.Title,
		CustomerID:  invCustID(i),
		Customer:    custMap[invCustID(i)],
		StatusID:    invStatusID(i),
		StatusName:  statusName(statuses, i.StatusID),
		StatusColor: statusColor(statuses, i.StatusID),
	}
	if !i.InvoiceDate.IsZero() {
		r.InvoiceDate = displayDate(ctx, i.InvoiceDate)
	}
	if !i.DueDate.IsZero() {
		r.DueDate = displayDate(ctx, i.DueDate)
	}
	return r
}

func invCustID(i *ent.Invoice) int64 {
	if i.CustomerID == nil {
		return 0
	}
	return *i.CustomerID
}

func displayPayments(ctx context.Context, payments []services.Payment) []services.Payment {
	for i := range payments {
		payments[i].Date = displayStoredDate(ctx, payments[i].Date)
	}
	return payments
}

func invStatusID(i *ent.Invoice) int64 {
	if i.StatusID == nil {
		return 0
	}
	return *i.StatusID
}

func (h *InvoiceHandler) invoiceStatusByName(ctx context.Context, name string) (*ent.Status, error) {
	status, err := h.statusSvc.FindByName(ctx, "invoice", name)
	if err != nil {
		return nil, fmt.Errorf("invoice status %q not found", name)
	}
	return status, nil
}

func (h *InvoiceHandler) invoiceHasStatus(ctx context.Context, i *ent.Invoice, names ...string) bool {
	current := statusName(h.statusesForSelect(ctx), i.StatusID)
	for _, name := range names {
		if strings.EqualFold(current, name) {
			return true
		}
	}
	return false
}

func (h *InvoiceHandler) updateInvoiceStatusAfterEmail(ctx context.Context, id int64) error {
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if h.invoiceHasStatus(ctx, i, "Paid", "Partially Paid", "Void") {
		return nil
	}
	sentStatus, err := h.invoiceStatusByName(ctx, "Sent")
	if err != nil {
		return err
	}
	return h.svc.SetStatus(ctx, id, sentStatus.ID)
}

func (h *InvoiceHandler) updateInvoiceStatusAfterPayment(ctx context.Context, id int64) error {
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if h.invoiceHasStatus(ctx, i, "Void") {
		return nil
	}
	total, paid, err := services.InvoiceAmountDue(i)
	if err != nil {
		return err
	}
	statusName := "Partially Paid"
	if paid <= 0 {
		statusName = "Sent"
	} else if paid+0.005 >= total {
		statusName = "Paid"
	}
	newStatus, err := h.invoiceStatusByName(ctx, statusName)
	if err != nil {
		return err
	}
	return h.svc.SetStatus(ctx, id, newStatus.ID)
}

func (h *InvoiceHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	j, err := h.jobSvc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cs := middleware.CompanyFromContext(r.Context())
	defaultTaxRate := "0"
	if cs != nil {
		defaultTaxRate = cs.DefaultTaxRate
	}
	statuses := h.statusesForSelect(r.Context())
	var statusID int64
	if draft, _ := h.statusSvc.FindByName(r.Context(), "invoice", "Draft"); draft != nil {
		statusID = draft.ID
	}
	customers, _ := h.custSvc.ListAll(r.Context())
	jobs, _ := h.jobSvc.ListAll(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	items, _ := services.ParseLineItems(j.LineItems)
	data := templates.InvoiceFormPageData{
		Invoice: &templates.InvoiceDetail{
			CustomerID: j.CustomerID,
			JobID:      j.ID,
			StatusID:   statusID,
			Title:      j.JobType,
			Notes:      j.Notes,
			TaxRate:    defaultTaxRate,
		},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(r.Context()),
		ExistingItemsJSON: services.SerializeLineItems(items),
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
		CancelURL:         fmt.Sprintf("/jobs/%d", id),
	}
	templates.InvoiceForm(data).Render(r.Context(), w)
}

func (h *InvoiceHandler) CreateFromCustomer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if _, err := h.custSvc.GetByID(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	defaultTaxRate := "0"
	if cs := middleware.CompanyFromContext(r.Context()); cs != nil {
		defaultTaxRate = cs.DefaultTaxRate
	}
	statuses := h.statusesForSelect(r.Context())
	var statusID int64
	if draft, _ := h.statusSvc.FindByName(r.Context(), "invoice", "Draft"); draft != nil {
		statusID = draft.ID
	}
	customers, _ := h.custSvc.ListAll(r.Context())
	jobs, _ := h.jobSvc.ListAll(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	data := templates.InvoiceFormPageData{
		Invoice: &templates.InvoiceDetail{
			CustomerID: id,
			StatusID:   statusID,
			TaxRate:    defaultTaxRate,
		},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(r.Context()),
		ExistingItemsJSON: "[]",
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
		CancelURL:         fmt.Sprintf("/customers/%d", id),
	}
	templates.InvoiceForm(data).Render(r.Context(), w)
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
	if !h.authorizeInvoice(w, r, id, policyRead) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writePDFResponseWithDisposition(w, doc.Filename, r.URL.Query().Get("download") == "1", func(w io.Writer) error {
		_, err := w.Write(doc.Data)
		return err
	})
}

func (h *InvoiceHandler) PreviewPDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.authorizeInvoice(w, r, id, policyRead) {
		return
	}
	i, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	templates.DocumentPreview(templates.DocumentPreviewData{
		ObjectType:  "invoice",
		ObjectID:    id,
		Title:       i.Title,
		BackURL:     fmt.Sprintf("/invoices/%d", id),
		PDFURL:      fmt.Sprintf("/invoices/%d/pdf", id),
		SaveURL:     fmt.Sprintf("/invoices/%d/pdf/save", id),
		EmailURL:    fmt.Sprintf("/invoices/%d/email", id),
		DownloadURL: fmt.Sprintf("/invoices/%d/pdf?download=1", id),
		Archived:    i.DeletedAt != nil,
	}).Render(r.Context(), w)
}

func (h *InvoiceHandler) SavePDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if doc.Archived {
		http.Error(w, "archived records are read-only", http.StatusForbidden)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	_, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, "invoice", id, doc, u.ID)
	if err != nil {
		internalServerError(w, r, "save invoice pdf", err)
		return
	}
	h.activitySvc.Record(r.Context(), u.ID, "pdf_saved", "invoice", id, map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d/pdf/preview?flash=PDF+saved", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) Email(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if doc.Archived {
		http.Error(w, "archived records are read-only", http.StatusForbidden)
		return
	}
	cs := middleware.CompanyFromContext(r.Context())
	data := templates.DocumentEmailData{
		ObjectType: "invoice",
		ObjectID:   id,
		Title:      doc.Title,
		BackURL:    fmt.Sprintf("/invoices/%d/pdf/preview", id),
		ActionURL:  fmt.Sprintf("/invoices/%d/email", id),
		To:         doc.CustomerEmail,
		CC:         emailAutoCC(cs),
	}
	data.Subject, data.Body = documentEmailDefaults("invoice", doc, cs)
	if r.Method == http.MethodGet {
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	data.To = r.FormValue("to")
	data.CC = r.FormValue("cc")
	data.BCC = r.FormValue("bcc")
	data.Subject = r.FormValue("subject")
	data.Body = r.FormValue("body")
	recipients, err := services.ParseEmailRecipients(data.To, data.CC, data.BCC)
	if err != nil {
		data.Error = err.Error()
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	savedFile, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, "invoice", id, doc, u.ID)
	if err != nil {
		data.Error = err.Error()
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	if err := h.emailSvc.SendEmailWithAttachmentTo(r.Context(), recipients, data.Subject, data.Body, filename, "application/pdf", doc.Data); err != nil {
		_ = h.fileSvc.Delete(r.Context(), savedFile.ID)
		data.Error = err.Error()
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	if err := h.updateInvoiceStatusAfterEmail(r.Context(), id); err != nil {
		data.Error = err.Error()
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	h.activitySvc.Record(r.Context(), u.ID, "email_sent", "invoice", id, map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "to": data.To, "cc": data.CC, "bcc_count": len(recipients.BCC), "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+emailed", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) invoicePDFDocument(ctx context.Context, id int64) (documentPDF, error) {
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return documentPDF{}, err
	}
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	var customer *ent.Customer
	if i.CustomerID != nil && *i.CustomerID > 0 {
		c, _ := h.custSvc.GetByID(ctx, *i.CustomerID)
		customer = c
	}
	var job *ent.Job
	var asset *ent.Asset
	if i.JobID != nil && *i.JobID > 0 {
		job, _ = h.jobSvc.GetByID(ctx, *i.JobID)
		if job != nil && job.AssetID != nil && *job.AssetID > 0 {
			asset, _ = h.assetSvc.GetByID(ctx, *job.AssetID)
		}
	}
	data, err := generatePDFBytes(func(w io.Writer) error {
		return services.GenerateInvoicePDF(w, i, customer, job, asset, statuses, middleware.CompanyFromContext(ctx))
	})
	if err != nil {
		return documentPDF{}, err
	}
	to := ""
	customerName := ""
	if customer != nil {
		to = customer.Email
		customerName = customer.DisplayName
	}
	jobName, jobType, jobSubtitle := documentJobFields(job)
	number := services.FormatInvoiceNumber(i.InvoiceNumber, middleware.CompanyFromContext(ctx))
	date := displayDate(ctx, time.Now())
	return documentPDF{Filename: number + ".pdf", Data: data, Title: i.Title, Number: number, CustomerEmail: to, CustomerName: customerName, JobName: jobName, JobType: jobType, JobSubtitle: jobSubtitle, Date: date, Archived: i.DeletedAt != nil}, nil
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
	if err := h.updateInvoiceStatusAfterPayment(r.Context(), id); err != nil {
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

func (h *InvoiceHandler) DeletePayment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	paymentID := chi.URLParam(r, "payment_id")
	payment, err := h.svc.DeletePayment(r.Context(), id, paymentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := h.updateInvoiceStatusAfterPayment(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "payment_deleted", "invoice", id, map[string]interface{}{
			"actor_name": u.Name,
			"amount":     fmt.Sprintf("%.2f", payment.Amount),
			"method":     payment.Method,
			"reference":  payment.Reference,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+deleted", id), http.StatusSeeOther)
}
