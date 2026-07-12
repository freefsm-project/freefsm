package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type conversionService interface {
	Convert(context.Context, conversion.Actor, conversion.ConvertRequest) (conversion.Result, error)
	Revert(context.Context, conversion.Actor, conversion.RevertRequest) (conversion.Result, error)
	ConversionEligibility(context.Context, conversion.Actor, int64) (conversion.Eligibility, error)
	RevertEligibility(context.Context, conversion.Actor, int64) (conversion.Eligibility, error)
	Timeline(context.Context, conversion.Actor, int64) ([]conversion.TimelineEntry, error)
}

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
	deliverySvc *delivery.Service
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	conversion  conversionService
	statusflow  *statusflow.Service
}

func NewEstimateHandler(svc *services.EstimateService, custSvc *services.CustomerService, jobSvc *services.JobService, statusSvc *services.StatusService, itemSvc *services.ItemService, invoiceSvc *services.InvoiceService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, deliverySvc *delivery.Service, activitySvc *services.ActivityService, policySvc *services.PolicyService, conversionSvc conversionService) *EstimateHandler {
	return &EstimateHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, statusSvc: statusSvc, itemSvc: itemSvc, invoiceSvc: invoiceSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, deliverySvc: deliverySvc, activitySvc: activitySvc, policySvc: policySvc, conversion: conversionSvc}
}

func conversionActor(r *http.Request) (conversion.Actor, bool) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		return conversion.Actor{}, false
	}
	return conversion.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, true
}

func (h *EstimateHandler) authorizeEstimate(w http.ResponseWriter, r *http.Request, id int64, action services.PolicyAction) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeEstimate, id), action) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
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
		rows[i] = estimateRow(r.Context(), e, statuses, custMap)
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
	if !h.authorizeEstimate(w, r, id, policyRead) {
		return
	}
	e, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	d := estimateToDetail(r.Context(), e, statuses)
	d.Statuses = statusOptions(statuses)
	u, _ := middleware.UserFromContext(r.Context())
	d.CanManage = u != nil && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeEstimate, id), policyUpdate)
	convertibleStatus := false
	for _, status := range statuses {
		if e.StatusID != nil && status.ID == *e.StatusID {
			convertibleStatus = true
			break
		}
	}
	if actor, ok := conversionActor(r); ok && convertibleStatus {
		eligibility, eligibilityErr := h.conversion.ConversionEligibility(r.Context(), actor, id)
		if eligibilityErr == nil {
			d.CanConvert = eligibility.Allowed
			d.ConvertKey = uuid.NewString()
			if !eligibility.Allowed && eligibility.Active == nil {
				d.ConvertBlocker = "This estimate's current status does not allow conversion."
			}
		} else if !errors.Is(eligibilityErr, conversion.ErrForbidden) && !errors.Is(eligibilityErr, conversion.ErrNotFound) {
			internalServerError(w, r, "check estimate conversion eligibility", eligibilityErr)
			return
		}
	}
	if e.CustomerID != nil && *e.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), *e.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(e)
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeEstimate, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "estimate")
	d.CustomFields = buildCustomFieldDisplay(defs, e.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), objectref.New(objectref.TypeEstimate, id))
	d.FileList = templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "estimate"}
	if u != nil {
		history, historyErr := h.deliverySvc.History(r.Context(), u.CompanyID, delivery.DocumentRef{Type: "estimate", ID: id})
		if historyErr != nil {
			internalServerError(w, r, "load estimate delivery history", historyErr)
			return
		}
		d.Deliveries = deliveryHistoryRows(history)
	}
	ctx := middleware.WithPageHeaderTitle(r.Context(), e.Title)
	templates.EstimateShow(d).Render(ctx, w)
}

func (h *EstimateHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, objectref.New(objectref.TypeEstimate, id))
	if err != nil {
		internalServerError(w, r, "attach estimate tag", err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeEstimate, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/estimates/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *EstimateHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, objectref.New(objectref.TypeEstimate, id)); err != nil {
		internalServerError(w, r, "detach estimate tag", err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeEstimate, id))
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
		data, err := h.newEstimateForm(r.Context(), customerID)
		if err != nil {
			internalServerError(w, r, "build estimate editor", err)
			return
		}
		templates.EstimateForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	if !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}
	taxRate = taxRateForCustomer(r.Context(), h.custSvc, custID, taxRate)
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), taxRate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := services.EstimateCreateParams{
		CustomerID:   custID,
		JobID:        jobID,
		StatusID:     0,
		Title:        r.FormValue("title"),
		Notes:        r.FormValue("notes"),
		TaxRate:      taxRate,
		LineItems:    lineItems,
		CustomFields: parseCustomFieldValues(r),
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", objectref.New(objectref.TypeEstimate, result.ID), map[string]interface{}{
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
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	if r.Method == http.MethodGet {
		e, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statuses := h.statusesForSelect(r.Context())
		fd, err := h.formDataFromEstimate(r.Context(), e, statuses)
		if err != nil {
			internalServerError(w, r, "build estimate editor", err)
			return
		}
		templates.EstimateForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	if !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
	taxRate := r.FormValue("tax_rate")
	taxRatePtr := formPtr(taxRate)
	if taxRate == "" {
		t := "0"
		taxRatePtr = &t
	}
	if taxRatePtr != nil {
		t := taxRateForCustomer(r.Context(), h.custSvc, custID, *taxRatePtr)
		taxRatePtr = &t
	}
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), *taxRatePtr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := services.EstimateUpdateParams{
		CustomerID:   int64Ptr(custID),
		JobID:        &jobID,
		Title:        formPtr(r.FormValue("title")),
		Notes:        formPtr(r.FormValue("notes")),
		TaxRate:      taxRatePtr,
		CustomFields: strPtr(parseCustomFieldValues(r)),
	}
	params.LineItems = &lineItems
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	key, err := uuid.Parse(r.FormValue("idempotency_key"))
	if err != nil {
		http.Error(w, "valid idempotency UUID is required", http.StatusUnprocessableEntity)
		return
	}
	actor, ok := conversionActor(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	result, err := h.conversion.Convert(r.Context(), actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: key}, EstimateID: id})
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Estimate+converted+to+invoice", result.InvoiceID), http.StatusSeeOther)
}

func writeConversionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, conversion.ErrForbidden):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, conversion.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, conversion.ErrArchived), errors.Is(err, conversion.ErrIdempotencyConflict), errors.Is(err, conversion.ErrTransactionConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, conversion.ErrSettlement):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		internalServerError(w, r, "conversion operation", err)
	}
}

func (h *EstimateHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
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
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), u.ID, u.Role, id) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defaultTaxRate := taxRateForCustomer(r.Context(), h.custSvc, j.CustomerID, companyDefaultTaxRate(r.Context()))
	est, err := h.svc.CreateFromJob(r.Context(), id, h.statusSvc, defaultTaxRate)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", objectref.New(objectref.TypeEstimate, est.ID), map[string]interface{}{
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
	if !h.authorizeEstimate(w, r, id, policyRead) {
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
	if !h.authorizeEstimate(w, r, id, policyRead) {
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
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
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
		internalServerError(w, r, "save estimate pdf", err)
		return
	}
	h.activitySvc.Record(r.Context(), u.ID, "pdf_saved", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d/pdf/preview?flash=PDF+saved", id), http.StatusSeeOther)
}

func (h *EstimateHandler) Email(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
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
		ObjectType:     "estimate",
		ObjectID:       id,
		Title:          doc.Title,
		BackURL:        fmt.Sprintf("/estimates/%d/pdf/preview", id),
		ActionURL:      fmt.Sprintf("/estimates/%d/email", id),
		To:             doc.CustomerEmail,
		CC:             emailAutoCC(cs),
		IdempotencyKey: uuid.NewString(),
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
	data.IdempotencyKey = r.FormValue("idempotency_key")
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
	key, err := uuid.Parse(data.IdempotencyKey)
	if err != nil || data.Subject == "" || data.Body == "" {
		data.Error = "valid idempotency key, subject, and body are required"
		templates.DocumentEmailCompose(data).Render(r.Context(), w)
		return
	}
	_, err = h.deliverySvc.Queue(r.Context(), delivery.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, delivery.QueueRequest{Key: key, Document: delivery.DocumentRef{Type: "estimate", ID: id}, Snapshot: delivery.Snapshot{To: recipients.To, CC: recipients.CC, BCC: recipients.BCC, Subject: data.Subject, TextBody: data.Body, HTMLBody: safeEmailHTML(data.Body), PDF: doc.Data, PDFFilename: doc.Filename}})
	if err != nil {
		writeDeliveryError(w, r, err, data)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+email+queued", id), http.StatusSeeOther)
}

func (h *EstimateHandler) RetryDelivery(w http.ResponseWriter, r *http.Request) {
	retryDocumentDelivery(h.deliverySvc, "estimate", w, r)
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
	date := displayDate(ctx, time.Now())
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
		h.activitySvc.Record(r.Context(), u.ID, "archived", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.ID, "restored", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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

func (h *EstimateHandler) newEstimateForm(ctx context.Context, customerID int64) (templates.EstimateFormPageData, error) {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, "[]", defaultTaxRate)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	return templates.EstimateFormPageData{
		Estimate:          &templates.EstimateDetail{CustomerID: customerID, TaxRate: taxRateForCustomer(ctx, h.custSvc, customerID, defaultTaxRate)},
		IsNew:             true,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
	}, nil
}

func (h *EstimateHandler) formDataFromEstimate(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) (templates.EstimateFormPageData, error) {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobsForEditor(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	d := estimateToDetail(ctx, e, statuses)
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, e.LineItems, defaultTaxRate)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	return templates.EstimateFormPageData{
		Estimate:          &d,
		IsNew:             false,
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, e.CustomFields),
	}, nil
}

func (h *EstimateHandler) jobsForEditor(ctx context.Context) ([]*ent.Job, error) {
	u, _ := middleware.UserFromContext(ctx)
	if u != nil && (u.Role == "tech" || u.Role == "technician") {
		return h.jobSvc.ListAssignedAll(ctx, u.ID)
	}
	return h.jobSvc.ListAll(ctx)
}

func (h *EstimateHandler) authorizeDestinationJob(w http.ResponseWriter, r *http.Request, customerID, jobID int64) bool {
	if jobID <= 0 {
		u, _ := middleware.UserFromContext(r.Context())
		if u != nil && (u.Role == "tech" || u.Role == "technician") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return false
		}
		return true
	}
	j, err := h.jobSvc.GetByID(r.Context(), jobID)
	if err != nil || j.CustomerID != customerID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), u.ID, u.Role, jobID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func estimateToDetail(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) templates.EstimateDetail {
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
		d.ArchivedAt = displayDate(ctx, *e.DeletedAt)
	}
	return d
}

func estimateRow(ctx context.Context, e *ent.Estimate, statuses []*ent.Status, custMap map[int64]string) templates.EstimateRow {
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
		r.CreatedAt = displayDate(ctx, e.CreatedAt)
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
