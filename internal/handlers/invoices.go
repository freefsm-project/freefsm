package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
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
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	settlement  settlementService
	conversion  conversionService
}

func NewInvoiceHandler(svc *services.InvoiceService, custSvc *services.CustomerService, jobSvc *services.JobService, assetSvc *services.AssetService, statusSvc *services.StatusService, itemSvc *services.ItemService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, emailSvc *services.EmailService, activitySvc *services.ActivityService, policySvc *services.PolicyService, settlement settlementService, conversionSvc conversionService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc, custSvc: custSvc, jobSvc: jobSvc, assetSvc: assetSvc, statusSvc: statusSvc, itemSvc: itemSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, emailSvc: emailSvc, activitySvc: activitySvc, policySvc: policySvc, settlement: settlement, conversion: conversionSvc}
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
	u, _ := middleware.UserFromContext(r.Context())
	d.CanManage = u != nil && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeInvoice, id), policyUpdate)
	d.CanSettle = u != nil && (u.Role == "admin" || u.Role == "dispatcher")
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
		customer, _ := h.custSvc.GetByID(r.Context(), i.CustomerID)
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
	if d.CanSettle {
		actor, ok := settlementActor(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		d.Settlement, err = h.settlement.InvoiceSettlement(r.Context(), actor, id)
		if err != nil {
			writeSettlementError(w, r, err)
			return
		}
		credit, err := h.settlement.CustomerSettlement(r.Context(), actor, d.Settlement.CustomerID)
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
	d.PaymentKey, d.ApplyCreditKey = newOperationKey(), newOperationKey()
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeInvoice, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	d.CustomFields = buildCustomFieldDisplay(defs, i.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), objectref.New(objectref.TypeInvoice, id))
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
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, objectref.New(objectref.TypeInvoice, id))
	if err != nil {
		internalServerError(w, r, "attach invoice tag", err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeInvoice, id))
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
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, objectref.New(objectref.TypeInvoice, id)); err != nil {
		internalServerError(w, r, "detach invoice tag", err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeInvoice, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/invoices/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *InvoiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data, err := h.newInvoiceForm(r.Context())
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
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if custID <= 0 {
		http.Error(w, "customer is required", http.StatusBadRequest)
		return
	}
	jobID, _ := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	invoiceNumber, err := parseOptionalPositiveInt64(r.FormValue("invoice_number"), "invoice number")
	if err != nil {
		http.Error(w, err.Error(), 400)
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
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		if errors.Is(err, services.ErrNegativeInvoiceTotal) {
			http.Error(w, "invoice total must not be negative", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", objectref.New(objectref.TypeInvoice, result.ID), map[string]interface{}{
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
	if !h.authorizeInvoice(w, r, id, policyUpdate) {
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
	if !h.authorizeDestinationJob(w, r, custID, jobID) {
		return
	}
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	if selected := statusName(h.statusesForSelect(r.Context()), &statusID); strings.EqualFold(selected, "Void") {
		actor, ok := settlementActor(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		view, queryErr := h.settlement.InvoiceSettlement(r.Context(), actor, id)
		if queryErr != nil {
			writeSettlementError(w, r, queryErr)
			return
		}
		if view.SettledCents > 0 {
			http.Error(w, "cannot void an invoice with active settlement", http.StatusConflict)
			return
		}
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
	if taxRatePtr != nil {
		t := taxRateForCustomer(r.Context(), h.custSvc, custID, *taxRatePtr)
		taxRatePtr = &t
	}
	lineItems, err := decodeAndValidateLineItems(r.FormValue("line_items"), *taxRatePtr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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
	params.LineItems = &lineItems
	params.CustomFields = strPtr(parseCustomFieldValues(r))
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		if errors.Is(err, services.ErrNegativeInvoiceTotal) {
			http.Error(w, "invoice total must not be negative", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
	if inv.SettlementState != "paid" && !h.invoiceHasStatus(r.Context(), inv, "Void") {
		http.Error(w, "invoice can only be archived when paid or void", http.StatusConflict)
		return
	}
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.ID, "restored", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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
	if !h.invoiceHasDocumentRole(r.Context(), i, "draft") {
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
		h.activitySvc.Record(r.Context(), u.ID, "finalized", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{
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

func (h *InvoiceHandler) newInvoiceForm(ctx context.Context) (templates.InvoiceFormPageData, error) {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, "[]", defaultTaxRate)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	return templates.InvoiceFormPageData{
		Invoice:           &templates.InvoiceDetail{TaxRate: defaultTaxRate},
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

func (h *InvoiceHandler) formDataFromInvoice(ctx context.Context, i *ent.Invoice, statuses []*ent.Status) (templates.InvoiceFormPageData, error) {
	customers, _ := h.custSvc.ListAll(ctx)
	jobs, _ := h.jobsForEditor(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "invoice")
	defaultTaxRate := companyDefaultTaxRate(ctx)
	d := invoiceToDetail(ctx, i, statuses)
	bootstrap, err := lineItemEditorBootstrap(ctx, h.itemSvc, customers, i.LineItems, defaultTaxRate)
	if err != nil {
		return templates.InvoiceFormPageData{}, err
	}
	return templates.InvoiceFormPageData{
		Invoice:           &d,
		IsNew:             false,
		Customers:         customerOptions(customers),
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
		ID:          i.ID,
		Number:      i.InvoiceNumber,
		CustomerID:  invCustID(i),
		StatusID:    invStatusID(i),
		StatusName:  statusName,
		StatusColor: statusColor(statuses, i.StatusID),
		CanFinalize: statusDocumentRole(statuses, i.StatusID) == "draft",
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
		r.InvoiceDate = displayDate(ctx, i.InvoiceDate)
	}
	if !i.DueDate.IsZero() {
		r.DueDate = displayDate(ctx, i.DueDate)
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

func (h *InvoiceHandler) invoiceHasDocumentRole(ctx context.Context, i *ent.Invoice, role string) bool {
	return statusDocumentRole(h.statusesForSelect(ctx), i.StatusID) == role
}

func statusDocumentRole(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.DocumentRole
		}
	}
	return ""
}

func (h *InvoiceHandler) updateInvoiceStatusAfterEmail(ctx context.Context, id int64) error {
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if h.invoiceHasStatus(ctx, i, "Void") {
		return nil
	}
	sentStatus, err := h.invoiceStatusByName(ctx, "Sent")
	if err != nil {
		return err
	}
	return h.svc.SetStatus(ctx, id, sentStatus.ID)
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
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !h.policySvc.CanCreateDocumentForJob(r.Context(), u.ID, u.Role, id) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defaultTaxRate := taxRateForCustomer(r.Context(), h.custSvc, j.CustomerID, companyDefaultTaxRate(r.Context()))
	statuses := h.statusesForSelect(r.Context())
	var statusID int64
	if draft, _ := h.statusSvc.DraftForObjectType(r.Context(), "invoice"); draft != nil {
		statusID = draft.ID
	}
	customers, _ := h.custSvc.ListAll(r.Context())
	jobs, _ := h.jobsForEditor(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	bootstrap, err := lineItemEditorBootstrap(r.Context(), h.itemSvc, customers, j.LineItems, companyDefaultTaxRate(r.Context()))
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
		Customers:         customerOptions(customers),
		Jobs:              jobOptions(jobs),
		Statuses:          statusOptions(statuses),
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
		CancelURL:         fmt.Sprintf("/jobs/%d", id),
		FormAction:        fmt.Sprintf("/jobs/%d/invoices", id),
	}
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
	h.Create(w, r)
}

func (h *InvoiceHandler) jobsForEditor(ctx context.Context) ([]*ent.Job, error) {
	u, _ := middleware.UserFromContext(ctx)
	if u != nil && (u.Role == "tech" || u.Role == "technician") {
		return h.jobSvc.ListAssignedAll(ctx, u.ID)
	}
	return h.jobSvc.ListAll(ctx)
}

func (h *InvoiceHandler) authorizeDestinationJob(w http.ResponseWriter, r *http.Request, customerID, jobID int64) bool {
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
	defaultTaxRate := taxRateForCustomer(r.Context(), h.custSvc, id, companyDefaultTaxRate(r.Context()))
	statuses := h.statusesForSelect(r.Context())
	var statusID int64
	if draft, _ := h.statusSvc.DraftForObjectType(r.Context(), "invoice"); draft != nil {
		statusID = draft.ID
	}
	customers, _ := h.custSvc.ListAll(r.Context())
	jobs, _ := h.jobSvc.ListAll(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "invoice")
	bootstrap, err := lineItemEditorBootstrap(r.Context(), h.itemSvc, customers, "[]", companyDefaultTaxRate(r.Context()))
	if err != nil {
		internalServerError(w, r, "build invoice editor", err)
		return
	}
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
		ItemsJSON:         bootstrap.ItemsJSON,
		ExistingItemsJSON: bootstrap.ExistingItemsJSON,
		CustomersJSON:     bootstrap.CustomersJSON,
		CustomFields:      buildCustomFieldDisplay(defs, "[]"),
		CancelURL:         fmt.Sprintf("/customers/%d", id),
	}
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
	h.activitySvc.Record(r.Context(), u.ID, "pdf_saved", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "file_name": filename})
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
	h.activitySvc.Record(r.Context(), u.ID, "email_sent", objectref.New(objectref.TypeInvoice, id), map[string]interface{}{"entity_name": doc.Title, "actor_name": u.Name, "to": data.To, "cc": data.CC, "bcc_count": len(recipients.BCC), "file_name": filename})
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Invoice+emailed", id), http.StatusSeeOther)
}

func (h *InvoiceHandler) invoicePDFDocument(ctx context.Context, id int64) (documentPDF, error) {
	i, err := h.svc.GetByID(ctx, id)
	if err != nil {
		return documentPDF{}, err
	}
	statuses, _ := h.statusSvc.ByObjectType(ctx, "invoice")
	actor, ok := settlementActor(ctx)
	if !ok {
		return documentPDF{}, errors.New("settlement actor is required")
	}
	settlementView, err := h.settlement.InvoiceSettlement(ctx, actor, id)
	if err != nil {
		return documentPDF{}, fmt.Errorf("load invoice settlement: %w", err)
	}
	var customer *ent.Customer
	if i.CustomerID > 0 {
		c, _ := h.custSvc.GetByID(ctx, i.CustomerID)
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
		return services.GenerateInvoicePDF(w, i, customer, job, asset, statuses, middleware.CompanyFromContext(ctx), services.InvoicePDFSettlement{
			AmountPaidCents: settlementView.SettledCents,
			AmountDueCents:  settlementView.AmountDueCents,
		})
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
	result, err := h.settlement.RecordPayment(r.Context(), actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, InvoiceID: id, AmountCents: amount, Method: settlement.PaymentMethod(r.FormValue("method")), ReceivedDate: settlement.Date(r.FormValue("date")), Reference: r.FormValue("reference"), Notes: r.FormValue("notes")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+recorded%%3A+%%24%.2f+applied", id, float64(result.AppliedCents)/100), http.StatusSeeOther)
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
