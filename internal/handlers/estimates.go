package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/saveaction"
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
		templates.EstimatesListContent(data).Render(r.Context(), w)
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
	d.CanManage = templates.DetailCan(r.Context(), "edit")
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
		customer, _ := customerForDocument(r.Context(), h.custSvc, *e.CustomerID, valueInt64(e.JobID))
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(e)
	if !templates.DetailCan(r.Context(), "pricing") {
		d.LineItems = nil
		d.TaxRate = ""
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeEstimate, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "estimate")
	d.CustomFields = buildCustomFieldDisplay(defs, e.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeEstimate, id))
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
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeEstimate, id))
	if err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeEstimate, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/estimates/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *EstimateHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeEstimate, id)); err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeEstimate, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
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
	if strings.TrimSpace(r.FormValue("title")) == "" {
		h.invalidEstimateForm(w, r, "Title is required")
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	if !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
	if err := initializeMissingDocumentFields(r, "estimate", custID, jobID); err != nil {
		authorizationError(w, r, err)
		return
	}
	if !validDocumentDate(r.FormValue("valid_through"), false) {
		h.invalidEstimateForm(w, r, "Enter a valid Valid Through date or leave it blank")
		return
	}
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}
	taxRate, err := taxRateForCustomer(r.Context(), h.custSvc, custID, taxRate, jobID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), taxRate)
	if err != nil {
		h.invalidEstimateForm(w, r, err.Error())
		return
	}

	params := services.EstimateCreateParams{
		ValidThrough: parseDatePtr(r.FormValue("valid_through"), time.UTC),
		CustomerID:   custID,
		JobID:        jobID,
		StatusID:     0,
		Title:        r.FormValue("title"),
		Notes:        r.FormValue("notes"),
		Terms:        r.FormValue("terms"),
		TaxRate:      taxRate,
		LineItems:    lineItems,
		CustomFields: parseCustomFieldValues(r),
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "estimate", err) {
			return
		}
		authorizationError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeEstimate, result.ID), map[string]interface{}{
			"entity_name": result.Title,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/estimates?flash=Estimate+created", http.StatusSeeOther)
}

func (h *EstimateHandler) invalidEstimateForm(w http.ResponseWriter, r *http.Request, message string) {
	saveaction.RejectBeforeWrite(r.Context())
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	data, err := h.newEstimateForm(r.Context(), customerID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	data.Estimate.Title = r.FormValue("title")
	data.Estimate.Notes = r.FormValue("notes")
	data.Estimate.Terms = r.FormValue("terms")
	data.Estimate.TaxRate = r.FormValue("tax_rate")
	data.Estimate.ValidThrough = r.FormValue("valid_through")
	data.Estimate.JobID, _ = strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	data.ExistingItemsJSON = submittedLineItems(r.FormValue("line_items"))
	data.PreserveEntered = true
	defs, err := h.defSvc.ListForObjectType(r.Context(), "estimate")
	if err != nil {
		internalServerError(w, r, "load estimate fields", err)
		return
	}
	data.CustomFields = buildCustomFieldDisplay(defs, parseCustomFieldValues(r))
	data.DefaultLineDescription = r.FormValue("default_line_description")
	data.Errors = map[string]string{"title": message}
	w.WriteHeader(422)
	render(w, r, templates.EstimateForm(data))
}

func (h *EstimateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if request, ok := authorization.FromContext(r.Context()); ok {
		if err := request.Service.DetailsAccess(r.Context(), request.Actor, "estimate", id); err != nil {
			authorizationError(w, r, err)
			return
		}
	} else {
		authorizationError(w, r, authorization.ErrForbidden)
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
	current, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pricingSubmitted := r.Form.Has("tax_rate") || r.Form.Has("line_items")
	contextChanged := custID != valueInt64(current.CustomerID) || jobID != valueInt64(current.JobID)
	if (pricingSubmitted || contextChanged) && !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
	taxRate := r.FormValue("tax_rate")
	taxRatePtr := formPtr(taxRate)
	if taxRate == "" {
		t := "0"
		taxRatePtr = &t
	}
	if taxRatePtr != nil && pricingSubmitted {
		t, err := taxRateForCustomer(r.Context(), h.custSvc, custID, *taxRatePtr, jobID)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		taxRatePtr = &t
	}
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), *taxRatePtr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := services.EstimateUpdateParams{
		ValidThrough:      parseDatePtr(r.FormValue("valid_through"), time.UTC),
		ClearValidThrough: r.Form.Has("valid_through") && r.FormValue("valid_through") == "",
		CustomerID:        int64Ptr(custID),
		JobID:             &jobID,
		Title:             formPtr(r.FormValue("title")),
		Notes:             formPtr(r.FormValue("notes")),
		Terms:             strPtr(r.FormValue("terms")),
		TaxRate:           taxRatePtr,
		CustomFields:      parseCustomFieldPatch(r),
	}
	if !r.Form.Has("terms") {
		params.Terms = nil
	}
	if r.Form.Has("valid_through") && !validDocumentDate(r.FormValue("valid_through"), false) {
		http.Error(w, "Invalid Valid Through date", 422)
		return
	}
	params.LineItems = &lineItems
	if !pricingSubmitted {
		params.TaxRate = nil
		params.LineItems = nil
	}
	if !contextChanged {
		params.CustomerID = nil
		params.JobID = nil
	}
	result, err := h.svc.SaveDetails(r.Context(), id, params)
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "estimate", err) {
			return
		}
		authorizationError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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
	result, err := h.conversion.Convert(r.Context(), actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: key}, EstimateID: id, InvoiceFields: parseCustomFieldPatch(r)})
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Estimate+converted+to+invoice", result.InvoiceID), http.StatusSeeOther)
}

func writeConversionError(w http.ResponseWriter, r *http.Request, err error) {
	var fieldErr *customfields.FieldError
	if errors.As(err, &fieldErr) {
		http.Error(w, fieldErr.Error(), http.StatusUnprocessableEntity)
		return
	}
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) || errors.Is(err, authorization.ErrConflict) {
		authorizationError(w, r, err)
		return
	}
	switch {
	case errors.Is(err, conversion.ErrForbidden):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, conversion.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, conversion.ErrArchived), errors.Is(err, conversion.ErrIdempotencyConflict), errors.Is(err, conversion.ErrTransactionConflict), errors.Is(err, conversion.ErrHistoricalFieldReview):
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
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), id, objectref.TypeEstimate) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defaultTaxRate, err := taxRateForCustomer(r.Context(), h.custSvc, j.CustomerID, companyDefaultTaxRate(r.Context()), j.ID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	est, err := h.svc.CreateFromJob(r.Context(), id, h.statusSvc, defaultTaxRate, parseCustomFieldValues(r))
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "estimate", err) {
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeEstimate, est.ID), map[string]interface{}{
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
	if documentPDFVersions(w, r, "estimate", id, "download", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.estimatePDFDocument(ctx, id, input)
	}) {
		return
	}
	if !h.authorizeEstimate(w, r, id, policyRead) {
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id, PDFRenderInput{})
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
	if documentPDFVersions(w, r, "estimate", id, "preview", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.estimatePDFDocument(ctx, id, input)
	}) {
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
	if documentPDFVersions(w, r, "estimate", id, "save", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.estimatePDFDocument(ctx, id, input)
	}) {
		return
	}
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id, PDFRenderInput{})
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
	_, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, u.CompanyID, "estimate", id, doc, u.ID)
	if err != nil {
		internalServerError(w, r, "save estimate pdf", err)
		return
	}
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "pdf_saved", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d/pdf/preview?flash=PDF+saved", id), http.StatusSeeOther)
}

func (h *EstimateHandler) Email(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if selectedPDFEmail(w, r, "estimate", id, h.deliverySvc) {
		return
	}
	if !h.authorizeEstimate(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.estimatePDFDocument(r.Context(), id, PDFRenderInput{})
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
		data.HTMLBody, err = composeHTMLDefault(h.deliverySvc, r, "estimate", doc, cs)
		if err != nil {
			writeDeliveryError(w, r, err, data)
			return
		}
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
	data.HTMLBody = r.FormValue("html_body")
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
	_, err = h.deliverySvc.Queue(r.Context(), delivery.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, delivery.QueueRequest{Key: key, Document: delivery.DocumentRef{Type: "estimate", ID: id}, Snapshot: delivery.Snapshot{To: recipients.To, CC: recipients.CC, BCC: recipients.BCC, Subject: data.Subject, TextBody: data.Body, HTMLBody: selectedEmailHTML(data.Body, data.HTMLBody), PDF: doc.Data, PDFFilename: doc.Filename}})
	if err != nil {
		writeDeliveryError(w, r, err, data)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Estimate+email+queued", id), http.StatusSeeOther)
}

func (h *EstimateHandler) RetryDelivery(w http.ResponseWriter, r *http.Request) {
	retryDocumentDelivery(h.deliverySvc, "estimate", w, r)
}

func (h *EstimateHandler) estimatePDFDocument(ctx context.Context, id int64, input PDFRenderInput) (documentPDF, error) {
	cs := input.Presentation
	if cs == nil {
		cs = middleware.CompanyFromContext(ctx)
	}
	e, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return documentPDF{}, err
	}
	statuses, _ := h.statusSvc.ByObjectType(ctx, "estimate")
	var customer *ent.Customer
	if e.CustomerID != nil && *e.CustomerID > 0 {
		c, _ := customerForDocument(ctx, h.custSvc, *e.CustomerID, valueInt64(e.JobID))
		customer = c
	}
	var job *ent.Job
	if e.JobID != nil && *e.JobID > 0 {
		job, _ = h.jobSvc.GetByID(ctx, *e.JobID)
	}
	data, err := generatePDFBytes(func(w io.Writer) error {
		return services.GenerateEstimatePDF(w, e, customer, job, statuses, cs, input.FieldLines...)
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
	number := services.RecordedEstimateNumber(e, cs)
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
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeEstimate, id), map[string]interface{}{
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
	if middleware.CompanyFromContext(ctx) == nil {
		return templates.EstimateFormPageData{}, authorization.ErrConfigurationUnavailable
	}
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, "[]", defaultTaxRate)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	effectiveTax, err := taxRateForCustomer(ctx, h.custSvc, customerID, defaultTaxRate)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	detail := &templates.EstimateDetail{CustomerID: customerID, TaxRate: effectiveTax}
	var customerDefaults map[string]*string
	if customerID > 0 {
		if request, ok := authorization.FromContext(ctx); ok {
			customerDefaults, err = request.Service.DocumentDefaultInputs(ctx, request.Actor, "estimate", customerID, 0)
			if err != nil {
				return templates.EstimateFormPageData{}, err
			}
		}
	}
	defaults := services.ResolveDocumentDefaults(middleware.CompanyFromContext(ctx), "estimate", customerDefaults)
	detail.Notes = defaults.Notes
	detail.Terms = defaults.Terms
	detail.ValidThrough = defaults.DateFrom(time.Now().In(middleware.CompanyLocation(ctx)))
	lineDescription := defaults.LineDescription
	choices, err := documentDefaultChoices(ctx, "estimate", customers)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	return templates.EstimateFormPageData{
		DefaultsJSON:           choices,
		DefaultLineDescription: lineDescription,
		Estimate:               detail,
		IsNew:                  true,
		Customers:              customerOptions(customers),
		Jobs:                   jobOptions(jobs),
		Statuses:               statusOptions(statuses),
		ItemsJSON:              bootstrap.ItemsJSON,
		ExistingItemsJSON:      bootstrap.ExistingItemsJSON,
		CustomersJSON:          bootstrap.CustomersJSON,
		CustomFields:           buildCustomFieldInitialDisplay(defs),
	}, nil
}

func (h *EstimateHandler) formDataFromEstimate(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) (templates.EstimateFormPageData, error) {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobsForEditor(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "estimate")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	d := estimateToDetail(ctx, e, statuses)
	if request, ok := authorization.FromContext(ctx); ok {
		p, err := request.Service.Principal(ctx, request.Actor)
		if err != nil {
			return templates.EstimateFormPageData{}, err
		}
		if !p.Allows("customer.view", authorization.All) {
			defaultTaxRate = e.TaxRate
		}
	}
	options, err := documentCustomerOptions(ctx, customers, d.CustomerID, d.JobID)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, e.LineItems, defaultTaxRate)
	if err != nil {
		return templates.EstimateFormPageData{}, err
	}
	return templates.EstimateFormPageData{
		Estimate:          &d,
		IsNew:             false,
		Customers:         options,
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, e.CustomFields),
	}, nil
}

func (h *EstimateHandler) jobsForEditor(ctx context.Context) ([]*ent.Job, error) {
	if req, ok := authorization.FromContext(ctx); ok {
		p, err := req.Service.Principal(ctx, req.Actor)
		if err != nil {
			return nil, err
		}
		return h.jobSvc.ListAll(authorization.WithNavigationReads(ctx, p))
	}
	u, _ := middleware.UserFromContext(ctx)
	if u != nil && (u.Role == "tech" || u.Role == "technician") {
		return h.jobSvc.ListAssignedAll(ctx, u.ID)
	}
	return h.jobSvc.ListAll(ctx)
}

func (h *EstimateHandler) authorizeDestinationJob(w http.ResponseWriter, r *http.Request, customerID, jobID int64) bool {
	if _, ok := authorization.FromContext(r.Context()); ok {
		return authorizeFieldDocumentDestination(w, r, customerID, jobID)
	}
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
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), jobID, objectref.TypeEstimate) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func estimateToDetail(ctx context.Context, e *ent.Estimate, statuses []*ent.Status) templates.EstimateDetail {
	d := templates.EstimateDetail{
		NumberLabel: services.RecordedEstimateNumber(e, middleware.CompanyFromContext(ctx)),
		ID:          e.ID,
		CustomerID:  estCustID(e),
		StatusID:    estStatusID(e),
		StatusName:  statusName(statuses, e.StatusID),
		StatusColor: statusColor(statuses, e.StatusID),
		Title:       e.Title,
		Notes:       e.Notes,
		TaxRate:     e.TaxRate,
	}
	if e.Terms != nil {
		d.Terms = *e.Terms
	}
	d.TermsUnknown = e.Terms == nil
	if e.ValidThrough != nil {
		d.ValidThrough = e.ValidThrough.Format("2006-01-02")
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
