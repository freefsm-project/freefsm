package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type EstimateHandler struct {
	svc         *services.EstimateService
	custSvc     *services.CustomerService
	jobSvc      *services.JobService
	statusSvc   *services.StatusService
	itemSvc     *services.ItemService
	invoiceSvc  *services.InvoiceService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	fileSvc     *services.FileService
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
}

func NewEstimateHandler(svc *services.EstimateService, custSvc *services.CustomerService, jobSvc *services.JobService, statusSvc *services.StatusService, itemSvc *services.ItemService, invoiceSvc *services.InvoiceService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, emailSvc *services.EmailService, activitySvc *services.ActivityService) *EstimateHandler {
	return &EstimateHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, statusSvc: statusSvc, itemSvc: itemSvc, invoiceSvc: invoiceSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, emailSvc: emailSvc, activitySvc: activitySvc}
}

func (h *EstimateHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)

	var estimates []*ent.Estimate
	var total int
	var err error
	if customerID > 0 {
		estimates, total, err = h.svc.ListForCustomer(r.Context(), customerID, search, statusID, page, perPage)
	} else {
		estimates, total, err = h.svc.List(r.Context(), search, statusID, page, perPage)
	}
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
		CustomerID: customerID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
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
	if e.CustomerID != nil && *e.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), *e.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(e)
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "estimate", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "estimate")
	d.CustomFields = buildCustomFieldDisplay(defs, e.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), "estimate", id)
	d.FileList = templates.FileListPageData{Files: filesToRows(files), ObjectID: id, ObjectType: "estimate"}
	ctx := middleware.WithPageHeaderTitle(r.Context(), e.Title)
	templates.EstimateShow(d).Render(ctx, w)
}

func (h *EstimateHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, "estimate", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", "estimate", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "estimate", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/estimates/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *EstimateHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, "estimate", id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", "estimate", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "estimate", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/estimates/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *EstimateHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		templates.EstimateForm(h.newEstimateForm(r.Context(), customerID)).Render(r.Context(), w)
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
		CustomerID:   custID,
		JobID:        jobID,
		StatusID:     statusID,
		Title:        r.FormValue("title"),
		Notes:        r.FormValue("notes"),
		TaxRate:      taxRate,
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
		h.activitySvc.Record(r.Context(), u.ID, "created", "estimate", result.ID, map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
		})
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
		CustomerID:   int64Ptr(custID),
		JobID:        &jobID,
		StatusID:     int64Ptr(statusID),
		Title:        formPtr(r.FormValue("title")),
		Notes:        formPtr(r.FormValue("notes")),
		TaxRate:      taxRatePtr,
		CustomFields: strPtr(parseCustomFieldValues(r)),
	}
	if lineItems != nil {
		params.LineItems = &lineItems
	}
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "estimate", id, map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
		})
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
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "converted", "estimate", id, map[string]interface{}{
			"entity_name": inv.Title,
			"actor_name":  u.Name,
			"invoice_id":  inv.ID,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+created+from+estimate", inv.ID), http.StatusSeeOther)
}

func (h *EstimateHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	cs := middleware.CompanyFromContext(r.Context())
	defaultTaxRate := "0"
	if cs != nil {
		defaultTaxRate = cs.DefaultTaxRate
	}
	est, err := h.svc.CreateFromJob(r.Context(), id, h.statusSvc, defaultTaxRate)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "estimate", est.ID, map[string]interface{}{
			"entity_name": est.Title,
			"actor_name":  u.Name,
			"job_id":      id,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+created+from+job", est.ID), http.StatusSeeOther)
}

func (h *EstimateHandler) PDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writePDFResponseWithDisposition(w, doc.Filename, r.URL.Query().Get("download") == "1", func(w io.Writer) error {
		_, err := w.Write(doc.Data)
		return err
	})
}

func (h *EstimateHandler) PreviewPDF(w http.ResponseWriter, r *http.Request) {
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
	templates.DocumentPreview(templates.DocumentPreviewData{
		ObjectType:  "estimate",
		ObjectID:    id,
		Title:       e.Title,
		BackURL:     fmt.Sprintf("/estimates/%d", id),
		PDFURL:      fmt.Sprintf("/estimates/%d/pdf", id),
		SaveURL:     fmt.Sprintf("/estimates/%d/pdf/save", id),
		EmailURL:    fmt.Sprintf("/estimates/%d/email", id),
		DownloadURL: fmt.Sprintf("/estimates/%d/pdf?download=1", id),
		Archived:    e.DeletedAt != nil,
	}).Render(r.Context(), w)
}

func (h *EstimateHandler) SavePDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id)
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
	_, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, "estimate", id, doc, u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.activitySvc.Record(r.Context(), u.ID, "pdf_saved", "estimate", id, map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d/pdf/preview?flash=PDF+saved", id), http.StatusSeeOther)
}

func (h *EstimateHandler) Email(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id)
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
		ObjectType: "estimate",
		ObjectID:   id,
		Title:      doc.Title,
		BackURL:    fmt.Sprintf("/estimates/%d/pdf/preview", id),
		ActionURL:  fmt.Sprintf("/estimates/%d/email", id),
		To:         doc.CustomerEmail,
		CC:         emailAutoCC(cs),
	}
	data.Subject, data.Body = documentEmailDefaults("estimate", doc, cs)
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
	savedFile, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, "estimate", id, doc, u.ID)
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
	h.activitySvc.Record(r.Context(), u.ID, "email_sent", "estimate", id, map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "to": data.To, "cc": data.CC, "bcc_count": len(recipients.BCC), "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+emailed", id), http.StatusSeeOther)
}

func (h *EstimateHandler) estimatePDFDocument(ctx context.Context, id int64) (documentPDF, error) {
	e, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return documentPDF{}, err
	}
	statuses, _ := h.statusSvc.ByObjectType(ctx, "estimate")
	var customer *ent.Customer
	if e.CustomerID != nil && *e.CustomerID > 0 {
		c, _ := h.custSvc.GetByID(ctx, *e.CustomerID)
		customer = c
	}
	var job *ent.Job
	if e.JobID != nil && *e.JobID > 0 {
		job, _ = h.jobSvc.GetByID(ctx, *e.JobID)
	}
	data, err := generatePDFBytes(func(w io.Writer) error {
		return services.GenerateEstimatePDF(w, e, customer, job, statuses, middleware.CompanyFromContext(ctx))
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
	number := services.FormatEstimateNumber(id, middleware.CompanyFromContext(ctx))
	date := time.Now().In(middleware.CompanyLocation(ctx)).Format("Jan 2, 2006")
	return documentPDF{Filename: number + ".pdf", Data: data, Title: e.Title, Number: number, CustomerEmail: to, CustomerName: customerName, JobName: jobName, JobType: jobType, JobSubtitle: jobSubtitle, Date: date, Archived: e.DeletedAt != nil}, nil
}

func (h *EstimateHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	entityName := e.Title
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "estimate", id, map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/estimates?flash=Estimate+archived", http.StatusSeeOther)
}

func (h *EstimateHandler) Restore(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.Restore(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "restored", "estimate", id, map[string]interface{}{
			"entity_name": e.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/estimates/"+strconv.FormatInt(id, 10)+"?flash=Estimate+restored", http.StatusSeeOther)
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

func (h *EstimateHandler) newEstimateForm(ctx context.Context, customerID int64) templates.EstimateFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
	return templates.EstimateFormPageData{
		Estimate:          &templates.EstimateDetail{CustomerID: customerID},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         h.itemsCatalog(ctx),
		ExistingItemsJSON: "[]",
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
	}
}

func (h *EstimateHandler) formDataFromEstimate(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) templates.EstimateFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
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
		CustomFields:      buildCustomFieldDisplay(defs, e.CustomFields),
	}
}

func estimateToDetail(e *ent.Estimate, statuses []*ent.Status) templates.EstimateDetail {
	d := templates.EstimateDetail{
		ID:          e.ID,
		CustomerID:  estCustID(e),
		StatusID:    estStatusID(e),
		StatusName:  statusName(statuses, e.StatusID),
		StatusColor: statusColor(statuses, e.StatusID),
		Title:       e.Title,
		Notes:       e.Notes,
		TaxRate:     e.TaxRate,
	}
	if e.JobID != nil {
		d.JobID = *e.JobID
	}
	if e.DeletedAt != nil && !e.DeletedAt.IsZero() {
		d.ArchivedAt = e.DeletedAt.Format("Jan 2, 2006")
	}
	return d
}

func estimateRow(e *ent.Estimate, statuses []*ent.Status, custMap map[int64]string) templates.EstimateRow {
	r := templates.EstimateRow{
		ID:          e.ID,
		Title:       e.Title,
		CustomerID:  estCustID(e),
		Customer:    custMap[estCustID(e)],
		StatusID:    estStatusID(e),
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
