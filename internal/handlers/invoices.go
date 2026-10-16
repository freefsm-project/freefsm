package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
	deliverySvc *delivery.Service
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	settlement  settlementService
	conversion  conversionService
	statusflow  *statusflow.Service
}

func NewInvoiceHandler(svc *services.InvoiceService, custSvc *services.CustomerService, jobSvc *services.JobService, assetSvc *services.AssetService, statusSvc *services.StatusService, itemSvc *services.ItemService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, deliverySvc *delivery.Service, activitySvc *services.ActivityService, policySvc *services.PolicyService, settlement settlementService, conversionSvc conversionService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, assetSvc: assetSvc, statusSvc: statusSvc, itemSvc: itemSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, deliverySvc: deliverySvc, activitySvc: activitySvc, policySvc: policySvc, settlement: settlement, conversion: conversionSvc}
}

func (h *InvoiceHandler) authorizeInvoice(w http.ResponseWriter, r *http.Request, id int64, action services.PolicyAction) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeInvoice, id), action) {
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
		rows[i] = invoiceRow(r.Context(), h.effectiveInvoice(r.Context(), inv), statuses, custMap)
		if p, current := authorization.NavigationPrincipal(r.Context()); current && !p.Allows("settlement.view", authorization.All) {
			rows[i].SettlementState = ""
		}
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
		templates.InvoicesListContent(data).Render(r.Context(), w)
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
	d := invoiceToDetail(r.Context(), h.effectiveInvoice(r.Context(), i), statuses)
	// Display the effective projection, but select the preserved manual choice.
	d.StatusID = valueInt64(i.StatusID)
	d.Statuses = nil
	if statusCategory(statuses, i.StatusID) != string(statusflow.InvoiceDraft) {
		for _, status := range statuses {
			if status.CategoryKey != string(statusflow.InvoicePaid) && status.CategoryKey != string(statusflow.InvoicePartiallyPaid) {
				d.Statuses = append(d.Statuses, templates.SelectOption{Value: status.ID, Label: status.Name})
			}
		}
	}
	u, _ := middleware.UserFromContext(r.Context())
	d.CanManage = templates.DetailCan(r.Context(), "edit")
	d.CanSettle = templates.DetailCan(r.Context(), "payment")
	if actor, ok := conversionActor(r); ok && i.EstimateID != nil {
		eligibility, eligibilityErr := h.conversion.RevertEligibility(r.Context(), actor, id)
		if eligibilityErr == nil {
			d.Converted, d.CanRevert, d.RevertKey = true, eligibility.Allowed, uuid.NewString()
			for _, blocker := range eligibility.Blockers {
				d.RevertBlockers = append(d.RevertBlockers, conversionBlockerText(blocker))
			}
		} else if !errors.Is(eligibilityErr, conversion.ErrNotFound) && !errors.Is(eligibilityErr, conversion.ErrForbidden) {
			internalServerError(w, r, "check invoice revert eligibility", eligibilityErr)
			return
		}
	}
	if i.CustomerID > 0 {
		customer, _ := customerForDocument(r.Context(), h.custSvc, i.CustomerID, valueInt64(i.JobID))
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	if i.JobID != nil && *i.JobID > 0 && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeJob, *i.JobID), policyRead) {
		job, _ := h.jobSvc.GetByID(r.Context(), *i.JobID)
		if job != nil && job.AssetID != nil && *job.AssetID > 0 {
			if req, ok := authorization.FromContext(r.Context()); ok {
				links, err := req.Service.LinkedJobAssets(r.Context(), req.Actor, job.ID)
				if err != nil {
					authorizationError(w, r, err)
					return
				}
				for _, link := range links {
					if link.AssetID == *job.AssetID && link.Operational && link.CanNavigate {
						d.AssetID = link.AssetID
						d.AssetName = link.Number + " · " + link.Name
					}
				}
			}
		}
	}
	d.LineItems = h.svc.LineItems(i)
	if !templates.DetailCan(r.Context(), "pricing") {
		d.LineItems = nil
		d.TaxRate = ""
	}
	if templates.DetailCan(r.Context(), "settlement") {
		actor, ok := settlementActor(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		d.Settlement, err = h.settlement.GrantedInvoiceSettlement(r.Context(), actor, id)
		if err != nil {
			writeSettlementError(w, r, err)
			return
		}
		if templates.DetailCan(r.Context(), "apply-credit") {
			credit, err := h.settlement.CustomerMasterSettlement(r.Context(), actor, d.Settlement.CustomerID)
			if err != nil {
				writeSettlementError(w, r, err)
				return
			}
			for _, source := range credit.Sources {
				if source.AvailableCents > 0 {
					d.CreditSources = append(d.CreditSources, source)
				}
			}
		}
	}
	d.PaymentKey, d.ApplyCreditKey = newOperationKey(), newOperationKey()
	if templates.DetailCan(r.Context(), "payment") {
		if request, ok := authorization.FromContext(r.Context()); ok {
			var entry settlement.PaymentEntryContext
			entry, err = settlement.New(request.Service.Pool).PrepareBoundedPayment(r.Context(), request.Actor, id)
			if err != nil {
				writeSettlementError(w, r, err)
				return
			}
			d.PaymentMethods, d.PaymentEligible, d.PaymentDueCents = entry.Methods, entry.Eligible, entry.DueCents
		}
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeInvoice, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	d.CustomFields = buildCustomFieldDisplay(defs, i.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeInvoice, id))
	d.FileList = templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "invoice"}
	if u != nil {
		history, historyErr := h.deliverySvc.History(r.Context(), u.CompanyID, delivery.DocumentRef{Type: "invoice", ID: id})
		if historyErr != nil {
			internalServerError(w, r, "load invoice delivery history", historyErr)
			return
		}
		d.Deliveries = deliveryHistoryRows(history)
	}
	ctx := middleware.WithPageHeaderTitle(r.Context(), i.Title)
	templates.InvoiceShow(d).Render(ctx, w)
}

func (h *InvoiceHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeInvoice, id))
	if err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeInvoice, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/invoices/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *InvoiceHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeInvoice, id)); err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeInvoice, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/invoices/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *InvoiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		data, err := h.newInvoiceForm(r.Context(), customerID)
		if err != nil {
			internalServerError(w, r, "build invoice editor", err)
			return
		}
		templates.InvoiceForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if strings.TrimSpace(r.FormValue("title")) == "" {
		h.invalidInvoiceForm(w, r, "Title is required")
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if custID <= 0 {
		http.Error(w, "customer is required", http.StatusBadRequest)
		return
	}
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	if err := initializeMissingDocumentFields(r, "invoice", custID, jobID); err != nil {
		authorizationError(w, r, err)
		return
	}
	if !validDocumentDate(r.FormValue("invoice_date"), true) || !validDocumentDate(r.FormValue("due_date"), false) {
		h.invalidInvoiceForm(w, r, "Enter a valid invoice date and an optional valid due date")
		return
	}
	invoiceNumber, err := parseOptionalPositiveInt64(r.FormValue("invoice_number"), "invoice number")
	if err != nil {
		h.invalidInvoiceForm(w, r, err.Error())
		return
	}
	taxRate := r.FormValue("tax_rate")
	if taxRate == "" {
		taxRate = "0"
	}
	_, err = taxRateForCustomer(r.Context(), h.custSvc, custID, taxRate, jobID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), taxRate)
	if err != nil {
		h.invalidInvoiceForm(w, r, err.Error())
		return
	}

	loc := middleware.CompanyLocation(r.Context())
	params := services.InvoiceCreateParams{
		InvoiceNumber: invoiceNumber,
		CustomerID:    custID,
		JobID:         jobID,
		StatusID:      0,
		Title:         r.FormValue("title"),
		Notes:         r.FormValue("notes"),
		Terms:         r.FormValue("terms"),
		InvoiceDate:   parseDate(r.FormValue("invoice_date"), loc),
		DueDate:       parseDate(r.FormValue("due_date"), loc),
		TaxRate:       taxRate,
		LineItems:     lineItems,
		CustomFields:  parseCustomFieldValues(r),
	}
	adjustments, err := documentAdjustments(r)
	if err != nil {
		h.invalidInvoiceForm(w, r, err.Error())
		return
	}
	if adjustments != nil {
		params.Adjustments = *adjustments
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		if errors.Is(err, authorization.ErrCommercialInput) {
			h.invalidInvoiceForm(w, r, err.Error())
			return
		}
		if renderFieldSaveError(w, r, h.defSvc, "invoice", err) {
			return
		}
		if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) || errors.Is(err, authorization.ErrConflict) {
			authorizationError(w, r, err)
			return
		}
		if errors.Is(err, services.ErrNegativeInvoiceTotal) {
			h.invalidInvoiceForm(w, r, "Invoice total must not be negative")
			return
		}
		authorizationError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeInvoice, result.ID), map[string]interface{}{
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
	if request, ok := authorization.FromContext(r.Context()); ok {
		if err := request.Service.DetailsAccess(r.Context(), request.Actor, "invoice", id); err != nil {
			authorizationError(w, r, err)
			return
		}
	} else {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if r.Method == http.MethodGet {
		i, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statuses := h.statusesForSelect(r.Context())
		fd, err := h.formDataFromInvoice(r.Context(), i, statuses)
		if err != nil {
			internalServerError(w, r, "build invoice editor", err)
			return
		}
		templates.InvoiceForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if custID <= 0 {
		http.Error(w, "customer is required", http.StatusBadRequest)
		return
	}
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	current, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Form.Has("title") && strings.TrimSpace(r.FormValue("title")) == "" {
		http.Error(w, "Title is required", 422)
		return
	}
	if custID != current.CustomerID && jobID == valueInt64(current.JobID) {
		jobID = 0
	}
	pricingSubmitted := r.Form.Has("tax_rate") || r.Form.Has("line_items") || r.Form.Has("adjustments")
	contextChanged := custID != current.CustomerID || jobID != valueInt64(current.JobID)
	if (pricingSubmitted || contextChanged) && !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
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
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), *taxRatePtr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := services.InvoiceUpdateParams{
		CustomerDefaultsReviewed: r.FormValue("customer_defaults_reviewed") == "true",
		InvoiceNumber:            invoiceNumber,
		CustomerID:               int64Ptr(custID),
		JobID:                    &jobID,
		Title:                    formPtr(r.FormValue("title")),
		Notes:                    strPtr(r.FormValue("notes")),
		Terms:                    strPtr(r.FormValue("terms")),
		TaxRate:                  taxRatePtr,
	}
	if !r.Form.Has("terms") {
		params.Terms = nil
	}
	if !r.Form.Has("notes") {
		params.Notes = nil
	}
	loc := middleware.CompanyLocation(r.Context())
	if r.Form.Has("invoice_date") && !validDocumentDate(r.FormValue("invoice_date"), true) {
		http.Error(w, "Invoice Date is required and must be a valid date", 422)
		return
	}
	if d := r.FormValue("invoice_date"); d != "" {
		t := parseDate(d, loc)
		params.InvoiceDate = &t
	}
	if r.Form.Has("due_date") {
		d := r.FormValue("due_date")
		if !validDocumentDate(d, false) {
			http.Error(w, "Enter a valid Due Date or choose No Due Date", 422)
			return
		}
		var t time.Time
		if d != "" {
			t = parseDate(d, loc)
		}
		params.DueDate = &t
	}
	params.LineItems = &lineItems
	if !r.Form.Has("tax_rate") {
		params.TaxRate = nil
	}
	if !r.Form.Has("line_items") {
		params.LineItems = nil
	}
	if !contextChanged {
		params.CustomerID = nil
		params.JobID = nil
	}
	params.CustomFields = parseCustomFieldPatch(r)
	adjustments, err := documentAdjustments(r)
	if err != nil {
		h.invalidInvoiceForm(w, r, err.Error())
		return
	}
	if pricingSubmitted {
		params.Adjustments = adjustments
	}
	result, err := h.svc.SaveDetails(r.Context(), id, params)
	if err != nil {
		if errors.Is(err, authorization.ErrCommercialInput) {
			h.invalidInvoiceForm(w, r, err.Error())
			return
		}
		if renderFieldSaveError(w, r, h.defSvc, "invoice", err) {
			return
		}
		if errors.Is(err, services.ErrNegativeInvoiceTotal) {
			http.Error(w, "invoice total must not be negative", http.StatusBadRequest)
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
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
	if inv.SettlementState != "paid" && statusCategory(h.statusesForSelect(r.Context()), inv.StatusID) != string(statusflow.InvoiceVoid) {
		http.Error(w, "invoice can only be archived when paid or void", http.StatusConflict)
		return
	}
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
	_, err = h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	category, err := h.statusflow.CurrentCategory(r.Context(), u.CompanyID, statusflow.Invoice, id)
	if err != nil || category != statusflow.InvoiceDraft {
		http.Error(w, "only draft invoices can be finalized", http.StatusConflict)
		return
	}
	err = h.statusflow.FinalizeInvoice(r.Context(), statusflow.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, id)
	if err != nil {
		statusflowHTTPError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+finalized", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	return statuses
}

func (h *InvoiceHandler) effectiveInvoice(ctx context.Context, invoice *ent.Invoice) *ent.Invoice {
	if invoice == nil || invoice.CompanyID == nil || h.statusflow == nil {
		return invoice
	}
	effective, err := h.statusflow.EffectiveInvoiceStatus(ctx, *invoice.CompanyID, invoice.ID)
	if err != nil {
		return invoice
	}
	copy := *invoice
	copy.StatusID = &effective.ID
	return &copy
}

func (h *InvoiceHandler) newInvoiceForm(ctx context.Context, customerIDs ...int64) (templates.InvoiceFormPageData, error) {
	if middleware.CompanyFromContext(ctx) == nil {
		return templates.InvoiceFormPageData{}, authorization.ErrConfigurationUnavailable
	}
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobsForEditor(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	detail := &templates.InvoiceDetail{TaxRate: defaultTaxRate, StatusID: h.defaultInvoiceStatusID(ctx, statusflow.InvoiceDraft)}
	var customerDefaults map[string]*string
	if len(customerIDs) > 0 && customerIDs[0] > 0 {
		detail.CustomerID = customerIDs[0]
		if request, ok := authorization.FromContext(ctx); ok {
			var err error
			customerDefaults, err = request.Service.DocumentDefaultInputs(ctx, request.Actor, "invoice", detail.CustomerID, 0)
			if err != nil {
				return templates.InvoiceFormPageData{}, err
			}
		}
	}
	defaults := services.ResolveDocumentDefaults(middleware.CompanyFromContext(ctx), "invoice", customerDefaults)
	today := time.Now().In(middleware.CompanyLocation(ctx))
	detail.Notes = defaults.Notes
	detail.Terms = defaults.Terms
	detail.InvoiceDate = today.Format("2006-01-02")
	detail.DueDate = defaults.DateFrom(today)
	lineDescription := defaults.LineDescription
	bootstrap, err := commercialLineItemEditorBootstrap(ctx, h.itemSvc, customers, "[]", defaultTaxRate)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	choices, err := documentDefaultChoices(ctx, "invoice", customers)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	return templates.InvoiceFormPageData{
		DefaultsJSON:           choices,
		Invoice:                detail,
		DefaultLineDescription: lineDescription,
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

func (h *InvoiceHandler) invalidInvoiceForm(w http.ResponseWriter, r *http.Request, message string) {
	saveaction.RejectBeforeWrite(r.Context())
	data, err := h.newInvoiceForm(r.Context())
	if id, parseErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64); parseErr == nil && id > 0 {
		current, getErr := h.svc.GetByID(r.Context(), id)
		if getErr != nil {
			authorizationError(w, r, getErr)
			return
		}
		data, err = h.formDataFromInvoice(r.Context(), current, h.statusesForSelect(r.Context()))
	}
	if err != nil {
		internalServerError(w, r, "build invoice editor", err)
		return
	}
	data.Invoice.Title = r.FormValue("title")
	data.Invoice.Notes = r.FormValue("notes")
	data.Invoice.Terms = r.FormValue("terms")
	data.Invoice.InvoiceDate = r.FormValue("invoice_date")
	data.Invoice.DueDate = r.FormValue("due_date")
	data.Invoice.TaxRate = r.FormValue("tax_rate")
	data.Invoice.CustomerID, _ = strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	data.Invoice.JobID, _ = strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	data.Invoice.Number, _ = strconv.ParseInt(r.FormValue("invoice_number"), 10, 64)
	data.EnteredInvoiceNumber = r.FormValue("invoice_number")
	data.DefaultLineDescription = r.FormValue("default_line_description")
	data.ExistingItemsJSON = submittedLineItems(r.FormValue("line_items"))
	data.AdjustmentsJSON = submittedAdjustments(r.FormValue("adjustments"))
	data.PreserveEntered = true
	defs, err := h.defSvc.ListForObjectType(r.Context(), "invoice")
	if err != nil {
		internalServerError(w, r, "load invoice fields", err)
		return
	}
	data.CustomFields = buildCustomFieldDisplay(defs, parseCustomFieldValues(r))
	customers, err := h.custSvc.ListAll(r.Context())
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	options, err := documentCustomerOptions(r.Context(), customers, data.Invoice.CustomerID, data.Invoice.JobID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	data.Customers = options
	if strings.HasPrefix(r.URL.Path, "/jobs/") {
		data.FormAction = r.URL.Path
		data.CancelURL = fmt.Sprintf("/jobs/%d", data.Invoice.JobID)
	}
	data.Errors = map[string]string{"title": message}
	w.WriteHeader(422)
	render(w, r, templates.InvoiceForm(data))
}

func (h *InvoiceHandler) formDataFromInvoice(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) (templates.InvoiceFormPageData, error) {
	customers, _ := h.custSvc.ListAll(ctx)
	choices, err := documentDefaultChoices(ctx, "invoice", customers, i.ID)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	jobs, _ := h.jobsForEditor(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	d := invoiceToDetail(ctx, i, statuses)
	if request, ok := authorization.FromContext(ctx); ok {
		p, err := request.Service.Principal(ctx, request.Actor)
		if err != nil {
			return templates.InvoiceFormPageData{}, err
		}
		if !p.Allows("customer.view", authorization.All) {
			defaultTaxRate = i.TaxRate
		}
	}
	options, err := documentCustomerOptions(ctx, customers, i.CustomerID, d.JobID)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	bootstrap, err := commercialLineItemEditorBootstrap(ctx, h.itemSvc, customers, i.LineItems, defaultTaxRate)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	return templates.InvoiceFormPageData{
		Invoice:           &d,
		DefaultsJSON:      choices,
		IsNew:             false,
		Customers:         options,
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, i.CustomFields),
	}, nil
}

func invoiceToDetail(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) templates.InvoiceDetail {
	statusName := statusName(statuses, i.StatusID)
	d := templates.InvoiceDetail{
		Financial:   i.Financial,
		NumberLabel: services.RecordedInvoiceNumber(i, middleware.CompanyFromContext(ctx)),
		ID:          i.ID,
		Number:      i.InvoiceNumber,
		CustomerID:  invCustID(i),
		StatusID:    invStatusID(i),
		StatusName:  statusName,
		StatusColor: statusColor(statuses, i.StatusID),
		CanFinalize: statusCategory(statuses, i.StatusID) == string(statusflow.InvoiceDraft),
		Title:       i.Title,
		Notes:       i.Notes,
		TaxRate:     i.TaxRate,
	}
	if i.Terms != nil {
		d.Terms = *i.Terms
	}
	d.TermsUnknown = i.Terms == nil
	if i.JobID != nil {
		d.JobID = *i.JobID
	}
	if !i.InvoiceDate.IsZero() {
		d.InvoiceDate = i.InvoiceDate.Format("2006-01-02")
		d.InvoiceDateDisplay = services.FormatLocalDate(i.InvoiceDate, middleware.CompanyFromContext(ctx))
	}
	if !i.DueDate.IsZero() {
		d.DueDate = i.DueDate.Format("2006-01-02")
		d.DueDateDisplay = services.FormatLocalDate(i.DueDate, middleware.CompanyFromContext(ctx))
	}
	if i.DeletedAt != nil && !i.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *i.DeletedAt)
	}
	return d
}

func invoiceRow(ctx context.Context, i *ent.Invoice, statuses []*ent.Status, custMap map[int64]string) templates.InvoiceRow {
	r := templates.InvoiceRow{
		NumberLabel:     services.RecordedInvoiceNumber(i, middleware.CompanyFromContext(ctx)),
		ID:              i.ID,
		Number:          i.InvoiceNumber,
		Title:           i.Title,
		CustomerID:      invCustID(i),
		Customer:        custMap[invCustID(i)],
		StatusID:        invStatusID(i),
		StatusName:      statusName(statuses, i.StatusID),
		StatusColor:     statusColor(statuses, i.StatusID),
		SettlementState: i.SettlementState,
	}
	if !i.InvoiceDate.IsZero() {
		r.InvoiceDate = services.FormatLocalDate(i.InvoiceDate, middleware.CompanyFromContext(ctx))
	}
	if !i.DueDate.IsZero() {
		r.DueDate = services.FormatLocalDate(i.DueDate, middleware.CompanyFromContext(ctx))
	}
	return r
}

func invCustID(i *ent.Invoice) int64 {
	return i.CustomerID
}

func invStatusID(i *ent.Invoice) int64 {
	if i.StatusID == nil {
		return 0
	}
	return *i.StatusID
}

func (h *InvoiceHandler) defaultInvoiceStatusID(ctx context.Context, category statusflow.CategoryKey) int64 {
	user, _ := middleware.UserFromContext(ctx)
	if user == nil || h.statusflow == nil {
		return 0
	}
	status, err := h.statusflow.Default(ctx, user.CompanyID, statusflow.Invoice, category)
	if err != nil {
		return 0
	}
	return status.ID
}

func (h *InvoiceHandler) CreateFromJob(w http.ResponseWriter, r *http.Request) {
	// This installed POST prepares an editor; only CreateForJob persists it.
	saveaction.ReadOnly(r.Context())
	w.Header().Set("X-FreeFSM-Result", r.URL.Path)
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
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), id, objectref.TypeInvoice) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defaultTaxRate, err := taxRateForCustomer(r.Context(), h.custSvc, j.CustomerID, companyDefaultTaxRate(r.Context()), j.ID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	statusID := h.defaultInvoiceStatusID(r.Context(), statusflow.InvoiceDraft)
	customers, _ := h.custSvc.ListAll(r.Context())
	options, err := documentCustomerOptions(r.Context(), customers, j.CustomerID, j.ID)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	jobs, _ := h.jobsForEditor(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	bootstrap, err := commercialLineItemEditorBootstrap(r.Context(), h.itemSvc, customers, j.LineItems, defaultTaxRate)
	if err != nil {
		internalServerError(w, r, "build invoice editor", err)
		return
	}
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
		Customers:         options,
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldInitialDisplay(defs),
		CancelURL:         fmt.Sprintf("/jobs/%d", id),
		FormAction:        fmt.Sprintf("/jobs/%d/invoices", id),
	}
	var customerDefaults map[string]*string
	if request, ok := authorization.FromContext(r.Context()); ok {
		customerDefaults, err = request.Service.DocumentDefaultInputs(r.Context(), request.Actor, "invoice", j.CustomerID, j.ID)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
	}
	defaults := services.ResolveDocumentDefaults(middleware.CompanyFromContext(r.Context()), "invoice", customerDefaults)
	data.Invoice.Terms = defaults.Terms
	if data.Invoice.Notes == "" {
		data.Invoice.Notes = defaults.Notes
	}
	data.DefaultLineDescription = defaults.LineDescription
	today := time.Now().In(middleware.CompanyLocation(r.Context()))
	data.Invoice.InvoiceDate = today.Format("2006-01-02")
	data.Invoice.DueDate = defaults.DateFrom(today)
	templates.InvoiceForm(data).Render(r.Context(), w)
}

func (h *InvoiceHandler) CreateForJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	postedJobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if postedJobID != jobID || !h.authorizeDestinationJob(w, r, customerID, jobID) {
		if postedJobID != jobID {
			http.Error(w, "job does not match creation route", http.StatusForbidden)
		}
		return
	}
	h.Create(w, r.WithContext(authorization.WithCapturedItemSource(r.Context(), "job", jobID)))
}

func (h *InvoiceHandler) jobsForEditor(ctx context.Context) ([]*ent.Job, error) {
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

func (h *InvoiceHandler) authorizeDestinationJob(w http.ResponseWriter, r *http.Request, customerID, jobID int64) bool {
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
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), jobID, objectref.TypeInvoice) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
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
	defaultTaxRate, err := taxRateForCustomer(r.Context(), h.custSvc, id, companyDefaultTaxRate(r.Context()))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	data, err := h.newInvoiceForm(r.Context(), id)
	if err != nil {
		internalServerError(w, r, "build invoice editor", err)
		return
	}
	data.Invoice.TaxRate = defaultTaxRate
	data.CancelURL = fmt.Sprintf("/customers/%d", id)
	templates.InvoiceForm(data).Render(r.Context(), w)
}

func (h *InvoiceHandler) RevertToEstimate(w http.ResponseWriter, r *http.Request) {
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
	result, err := h.conversion.Revert(r.Context(), actor, conversion.RevertRequest{Operation: conversion.Operation{Key: key}, InvoiceID: id})
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/estimates/%d?flash=Invoice+reverted+to+estimate", result.EstimateID), http.StatusSeeOther)
}

func conversionBlockerText(blocker conversion.Blocker) string {
	switch blocker {
	case conversion.BlockerArchived:
		return "Restore this archived invoice before reverting."
	case conversion.BlockerActivePayment:
		return "Reverse all active payments before reverting."
	case conversion.BlockerActiveCreditApplication:
		return "Reverse all active credit applications before reverting."
	case conversion.BlockerUnresolvedPaymentCredit:
		return "Refund or apply all remaining overpayment credit before reverting."
	default:
		return "Financial settlement must be fully unwound before reverting."
	}
}

func (h *InvoiceHandler) PDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if documentPDFVersions(w, r, "invoice", id, "download", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.invoicePDFDocument(ctx, id, input)
	}) {
		return
	}
	if !h.authorizeInvoice(w, r, id, policyRead) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id, PDFRenderInput{})
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
	if documentPDFVersions(w, r, "invoice", id, "preview", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.invoicePDFDocument(ctx, id, input)
	}) {
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
	if documentPDFVersions(w, r, "invoice", id, "save", func(ctx context.Context, input PDFRenderInput) (documentPDF, error) {
		return h.invoicePDFDocument(ctx, id, input)
	}) {
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id, PDFRenderInput{})
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
	_, filename, err := saveVersionedDocumentPDF(r.Context(), h.fileSvc, u.CompanyID, "invoice", id, doc, u.ID)
	if err != nil {
		internalServerError(w, r, "save invoice pdf", err)
		return
	}
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "pdf_saved", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d/pdf/preview?flash=PDF+saved", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) Email(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if selectedPDFEmail(w, r, "invoice", id, h.deliverySvc) {
		return
	}
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
		return
	}
	doc, err := h.invoicePDFDocument(r.Context(), id, PDFRenderInput{})
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
		CC:         emailAutoCC(cs), IdempotencyKey: uuid.NewString(),
	}
	data.Subject, data.Body = documentEmailDefaults("invoice", doc, cs)
	if r.Method == http.MethodGet {
		data.HTMLBody, err = composeHTMLDefault(h.deliverySvc, r, "invoice", doc, cs)
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
	_, err = h.deliverySvc.Queue(r.Context(), delivery.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, delivery.QueueRequest{Key: key, Document: delivery.DocumentRef{Type: "invoice", ID: id}, Snapshot: delivery.Snapshot{To: recipients.To, CC: recipients.CC, BCC: recipients.BCC, Subject: data.Subject, TextBody: data.Body, HTMLBody: selectedEmailHTML(data.Body, data.HTMLBody), PDF: doc.Data, PDFFilename: doc.Filename}})
	if err != nil {
		writeDeliveryError(w, r, err, data)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+email+queued", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) RetryDelivery(w http.ResponseWriter, r *http.Request) {
	retryDocumentDelivery(h.deliverySvc, "invoice", w, r)
}

func (h *InvoiceHandler) invoicePDFDocument(ctx context.Context, id int64, input PDFRenderInput) (documentPDF, error) {
	cs := input.Presentation
	if cs == nil {
		cs = middleware.CompanyFromContext(ctx)
	}
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return documentPDF{}, err
	}
	i = h.effectiveInvoice(ctx, i)
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	var settlementView settlement.InvoiceSettlement
	if input.SettledCents != nil {
		settled := *input.SettledCents
		totals, err := services.FinancialTotals(i.Financial, i.LineItems, i.TaxRate)
		if err != nil {
			return documentPDF{}, err
		}
		if settled > totals.Total.MinorUnits() {
			return documentPDF{}, errors.New("invalid invoice settlement snapshot")
		}
		settlementView.SettledCents = settled
		settlementView.AmountDueCents = totals.Total.MinorUnits() - settled
	} else {
		actor, ok := settlementActor(ctx)
		if !ok {
			return documentPDF{}, errors.New("settlement actor is required")
		}
		settlementView, err = h.settlement.InvoiceSettlement(ctx, actor, id)
		if err != nil {
			return documentPDF{}, fmt.Errorf("load invoice settlement: %w", err)
		}
	}
	var customer *ent.Customer
	if i.CustomerID > 0 {
		c, _ := customerForDocument(ctx, h.custSvc, i.CustomerID, valueInt64(i.JobID))
		customer = c
	}
	var job *ent.Job
	// Only the reviewed association projection may supply Asset fields. A
	// retained compatibility pointer is not authority to fetch today's master.
	asset := input.Asset
	if i.JobID != nil && *i.JobID > 0 {
		job, _ = h.jobSvc.GetByID(ctx, *i.JobID)
	}
	data, err := generatePDFBytes(func(w io.Writer) error {
		return services.GenerateInvoicePDF(w, i, customer, job, asset, statuses, cs, services.InvoicePDFSettlement{
			AmountPaidCents: settlementView.SettledCents,
			AmountDueCents:  settlementView.AmountDueCents,
		}, input.FieldLines...)
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
	number := services.RecordedInvoiceNumber(i, cs)
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
	amount, err := parseCents(r.FormValue("amount"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	_, err = h.settlement.RecordBoundedPayment(r.Context(), actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, InvoiceID: id, AmountCents: amount, Method: settlement.PaymentMethod(r.FormValue("method")), ReceivedDate: settlement.Date(r.FormValue("date")), Reference: r.FormValue("reference"), Notes: r.FormValue("notes"), OtherDescription: r.FormValue("other_description")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		request, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", 401)
			return
		}
		receipt, err := settlement.New(request.Service.Pool).PaymentReceipt(r.Context(), request.Actor, r.FormValue("idempotency_key"))
		if err != nil {
			writeSettlementError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+receipt+confirmed", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) ReversePayment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	h.reverseSettlement(w, r, id, "payment_id", h.settlement.ReversePayment, "Payment+reversed")
}

func (h *InvoiceHandler) ApplyCredit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	amount, err := parseCents(r.FormValue("amount"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	creditID, err := parseUUID(r.FormValue("credit_id"), "credit source")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	result, err := h.settlement.ApplyCredit(r.Context(), actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, InvoiceID: id, CreditID: creditID, RequestedCents: amount, EffectiveDate: settlement.Date(r.FormValue("effective_date"))})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Customer+credit+applied%%3A+%%24%.2f", id, float64(result.AppliedCents)/100), 303)
}

func (h *InvoiceHandler) ReverseCreditApplication(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	h.reverseSettlement(w, r, id, "application_id", h.settlement.ReverseCreditApplication, "Credit+application+reversed")
}

func (h *InvoiceHandler) reverseSettlement(w http.ResponseWriter, r *http.Request, invoiceID int64, param string, fn func(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error), flash string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	opID, err := parseUUID(chi.URLParam(r, param), "settlement operation")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	_, err = fn(r.Context(), actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, ID: opID, InvoiceID: invoiceID, EffectiveDate: settlement.Date(r.FormValue("effective_date")), Reason: r.FormValue("reason")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=%s", invoiceID, flash), 303)
}
