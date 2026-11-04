package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CustomerHandler struct {
	svc         *services.CustomerService
	contactSvc  *services.CustomerContactService
	locationSvc *services.LocationService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	fileSvc     *services.FileService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	jobSvc      *services.JobService
	estimateSvc *services.EstimateService
	invoiceSvc  *services.InvoiceService
	statusSvc   *services.StatusService
	settlement  settlementService
	statusflow  *statusflow.Service
}

func NewCustomerHandler(svc *services.CustomerService, contactSvc *services.CustomerContactService, locationSvc *services.LocationService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, activitySvc *services.ActivityService, policySvc *services.PolicyService, jobSvc *services.JobService, estimateSvc *services.EstimateService, invoiceSvc *services.InvoiceService, statusSvc *services.StatusService, settlement settlementService) *CustomerHandler {
	return &CustomerHandler{svc: svc, contactSvc: contactSvc, locationSvc: locationSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, activitySvc: activitySvc, policySvc: policySvc, jobSvc: jobSvc, estimateSvc: estimateSvc, invoiceSvc: invoiceSvc, statusSvc: statusSvc, settlement: settlement}
}

func (h *CustomerHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	if size, _ := strconv.Atoi(r.URL.Query().Get("per_page")); size == 50 || size == 100 {
		perPage = size
	}
	sort := r.URL.Query().Get("sort")
	if sort != "name_desc" && sort != "updated" {
		sort = "name"
	}
	search := r.URL.Query().Get("search")
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", 403)
		return
	}
	archived := r.URL.Query().Get("archived") == "1"
	customers, total, err := request.Service.FindCustomers(r.Context(), request.Actor, search, archived, page, perPage, sort)
	if err != nil {
		authorizationError(w, r, err)
		return
	}

	rows := make([]templates.CustomerRow, len(customers))
	for i, c := range customers {
		rows[i] = customerRow(c.Customer)
		rows[i].Match = c.Match
		rows[i].Archived = c.Customer.DeletedAt != nil
	}

	data := templates.CustomerListPageData{
		Sort:       sort,
		Customers:  rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.PaginationTotalPages(total, perPage),
		Search:     search,
		Archived:   archived,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.CustomerListContent(data).Render(r.Context(), w)
		return
	}
	templates.CustomerIndex(data).Render(r.Context(), w)
}

func (h *CustomerHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	c, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u == nil || u.CompanyID <= 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	sectionErrors := map[string]bool{}
	sectionFailed := func(section string, err error) {
		if err != nil {
			sectionErrors[section] = true
			slog.ErrorContext(r.Context(), "load Customer dashboard section", "section", section, "error", err)
		}
	}
	tags, err := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeCustomer, c.ID))
	sectionFailed("Tags", err)
	var allTags []*ent.Tag
	if canAttachTag(r, "customer", c.ID) {
		allTags, err = h.tagSvc.ListAll(r.Context(), u.CompanyID)
		sectionFailed("Tags", err)
	}
	defs, err := h.defSvc.ListForObjectType(r.Context(), "customer")
	sectionFailed("Custom Fields", err)
	files, err := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeCustomer, c.ID))
	sectionFailed("Files", err)
	var locations []*ent.Location
	var contacts []*ent.CustomerContact
	if customerPermission(r, "location.view") {
		locations, err = h.locationSvc.ListByCustomer(r.Context(), c.ID)
		sectionFailed("Locations", err)
	}
	if customerPermission(r, "contact.view") {
		contacts, err = h.contactSvc.ListByCustomer(r.Context(), c.ID)
		sectionFailed("Contacts", err)
	}
	request, _ := authorization.FromContext(r.Context())
	var assetPeriods []authorization.CustomerAssetPeriod
	if customerPermission(r, "asset.view") {
		assetPeriods, err = request.Service.CustomerAssetHistory(r.Context(), request.Actor, c.ID, false)
		sectionFailed("Assets", err)
	}
	showServiceEntries := customerPermission(r, "asset.view") && customerPermission(r, "service_entry.view")
	var assetServiceHistory []templates.CustomerAssetServiceHistory
	if showServiceEntries && !sectionErrors["Assets"] {
		seen := map[int64]int{}
		for _, period := range assetPeriods {
			if index, ok := seen[period.AssetID]; ok {
				assetServiceHistory[index].Current = assetServiceHistory[index].Current || period.Current
				continue
			}
			entries, loadErr := request.Service.AssetServiceEntries(r.Context(), request.Actor, period.AssetID, c.ID)
			sectionFailed("Service Entries", loadErr)
			seen[period.AssetID] = len(assetServiceHistory)
			assetServiceHistory = append(assetServiceHistory, templates.CustomerAssetServiceHistory{Number: period.Number, Current: period.Current, Entries: entries})
		}
	}
	showFinancial := customerPermission(r, "customer.financial.view")
	var receivables templates.CustomerReceivables
	if showFinancial {
		receivables, err = customerReceivables(r.Context(), request, c.ID, middleware.CompanyLocation(r.Context()))
		sectionFailed("Receivables", err)
	}
	showCredit := customerPermission(r, "credit.view")
	var credit int64
	if showCredit {
		credit, err = request.Service.CustomerCreditTotal(r.Context(), request.Actor, c.ID)
		sectionFailed("Customer Credit", err)
	}
	showSettlement := showCredit && customerPermission(r, "settlement.view")
	var creditHistory settlement.CustomerSettlement
	if showSettlement {
		actor, _ := settlementActor(r.Context())
		creditHistory, err = h.settlement.CustomerMasterSettlement(r.Context(), actor, c.ID)
		sectionFailed("Customer Credit", err)
	}
	ctx := middleware.WithPageHeaderTitle(r.Context(), c.DisplayName)
	work, err := request.Service.CustomerRelatedWork(r.Context(), request.Actor, c.ID)
	sectionFailed("Related Work", err)
	leases, err := request.Service.RelatedLeases(r.Context(), request.Actor, "customer", c.ID)
	sectionFailed("Leases", err)
	templates.CustomerShow(templates.CustomerShowPageData{
		Leases:              leases,
		ShowServiceEntries:  showServiceEntries,
		AssetServiceHistory: assetServiceHistory,
		SectionErrors:       sectionErrors,
		AssetPeriods:        assetPeriods,
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
		ShowSettlement:      showSettlement,
		Settlement:          creditHistory,
		CanRefund:           customerPermission(r, "credit.refund"),
		CanReverseRefund:    customerPermission(r, "credit.refund.reverse"),
		Receivables:         receivables,
		ShowCredit:          showCredit,
		CreditCents:         credit,
		RelatedWork:         work,
		ShowFinancial:       showFinancial,
		Customer:            customerToDetail(r.Context(), c),
		Locations:           locationRows(locations),
		Contacts:            contactRows(contacts),
		Tags:                tagsToRows(tags),
		AllTags:             tagsToRows(allTags),
		CustomFields:        buildCustomFieldDisplay(defs, c.CustomFields),
		FileList:            templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: c.ID, ObjectType: "customer"},
		RefundKey:           newOperationKey(),
	}).Render(ctx, w)
}

func (h *CustomerHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		data := newFormData()
		data.OperationID = newOperationKey()
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
		data.CustomFields = buildCustomFieldInitialDisplay(defs)
		templates.CustomerForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if request, ok := authorization.FromContext(r.Context()); ok {
		result, err := request.Service.SaveCustomer(r.Context(), request.Actor, 0, customerInput(r), authorization.WebAction{ID: r.FormValue("operation_id")})
		if err != nil {
			if h.fieldError(w, r, 0, err) {
				return
			}
			h.customerSaveError(w, r, 0, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/customers/%d/edit", result.ID), http.StatusSeeOther)
		return
	}
	params := services.CustomerCreateParams{
		FirstName:       r.FormValue("first_name"),
		LastName:        r.FormValue("last_name"),
		DisplayName:     r.FormValue("display_name"),
		Email:           r.FormValue("email"),
		Phone:           r.FormValue("phone"),
		CompanyName:     r.FormValue("company_name"),
		Notes:           r.FormValue("notes"),
		Status:          r.FormValue("status"),
		AccountType:     r.FormValue("account_type"),
		BillingAddress1: r.FormValue("billing_address_1"),
		BillingAddress2: r.FormValue("billing_address_2"),
		BillingCity:     r.FormValue("billing_city"),
		BillingState:    r.FormValue("billing_state"),
		BillingZipCode:  r.FormValue("billing_zip_code"),
		TaxExempt:       r.FormValue("tax_exempt") == "true",
		CustomFields:    parseCustomFieldValues(r),
	}
	if params.Status == "" {
		params.Status = "lead"
	}
	if params.AccountType == "" {
		params.AccountType = "individual"
	}
	result, err := h.svc.Create(r.Context(), u.CompanyID, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeCustomer, result.ID), map[string]interface{}{
		"entity_name": result.DisplayName,
		"actor_name":  u.Name,
	})
	http.Redirect(w, r, fmt.Sprintf("/customers/%d/edit?flash=%s", result.ID, url.QueryEscape("Customer created. Locations and contacts can now be added.")), http.StatusSeeOther)
}

func (h *CustomerHandler) Update(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		c, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := formDataFromCustomer(r.Context(), c)
		data.OperationID = newOperationKey()
		data.Revision = authorization.CustomerRevision(c)
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
		data.CustomFields = buildCustomFieldDisplay(defs, c.CustomFields)
		locations, _ := h.locationSvc.ListByCustomer(r.Context(), c.ID)
		contacts, _ := h.contactSvc.ListByCustomer(r.Context(), c.ID)
		tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeCustomer, c.ID))
		allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
		data.Locations = locationRows(locations)
		data.Contacts = contactRows(contacts)
		data.Tags = tagsToRows(tags)
		data.AllTags = tagsToRows(allTags)
		templates.CustomerForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if request, ok := authorization.FromContext(r.Context()); ok {
		_, err := request.Service.SaveCustomer(r.Context(), request.Actor, id, customerInput(r), authorization.WebAction{ID: r.FormValue("operation_id"), Revision: r.FormValue("revision")})
		if err != nil {
			if h.fieldError(w, r, id, err) {
				return
			}
			h.customerSaveError(w, r, id, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/customers/%d", id), http.StatusSeeOther)
		return
	}
	params := services.CustomerUpdateParams{
		FirstName:       formPtr(r.FormValue("first_name")),
		LastName:        formPtr(r.FormValue("last_name")),
		DisplayName:     formPtr(r.FormValue("display_name")),
		Email:           formPtr(r.FormValue("email")),
		Phone:           formPtr(r.FormValue("phone")),
		CompanyName:     formPtr(r.FormValue("company_name")),
		Notes:           formPtr(r.FormValue("notes")),
		Status:          formPtr(r.FormValue("status")),
		AccountType:     formPtr(r.FormValue("account_type")),
		BillingAddress1: formPtr(r.FormValue("billing_address_1")),
		BillingAddress2: formPtr(r.FormValue("billing_address_2")),
		BillingCity:     formPtr(r.FormValue("billing_city")),
		BillingState:    formPtr(r.FormValue("billing_state")),
		BillingZipCode:  formPtr(r.FormValue("billing_zip_code")),
		TaxExempt:       boolPtr(r.FormValue("tax_exempt") == "true"),
		CustomFields:    strPtr(parseCustomFieldValues(r)),
	}
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"entity_name": result.DisplayName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/customers/"+strconv.FormatInt(id, 10)+"?flash=Customer+updated", http.StatusSeeOther)
}

func (h *CustomerHandler) customerSaveError(w http.ResponseWriter, r *http.Request, id int64, err error) {
	var validation *authorization.CustomerValidationError
	if !errors.As(err, &validation) && !errors.Is(err, authorization.ErrConflict) {
		authorizationError(w, r, err)
		return
	}
	// The failed command may have waited behind a revocation. Reload current
	// authority before retaining inputs or presenting an authorized comparison.
	if !customerPermission(r, "customer.view") || (id != 0 && !customerPermission(r, "customer.edit")) {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	c := &ent.Customer{ID: id}
	encoded, _ := json.Marshal(customerInput(r))
	if err := json.Unmarshal(encoded, c); err != nil {
		internalServerError(w, r, "retain Customer input", err)
		return
	}
	data := formDataFromCustomer(r.Context(), c)
	initializeCustomerErrorIdentity(&data, r, id)
	data.Errors = map[string]string{}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
	data.CustomFields = buildCustomFieldDisplay(defs, parseCustomFieldValues(r))
	code := http.StatusUnprocessableEntity
	if validation != nil {
		data.Errors[validation.Field] = validation.Message
	} else {
		code = http.StatusConflict
		data.Errors["display_name"] = "This save conflicts with the current record or an earlier action. Compare, revise and save, or discard your changes."
		if id != 0 {
			current, loadErr := h.svc.GetByID(r.Context(), id)
			if loadErr != nil {
				authorizationError(w, r, authorization.ErrNotFound)
				return
			}
			d := customerToDetail(r.Context(), current)
			data.Conflict = &d
			data.ConflictFields = buildCustomFieldDisplay(defs, current.CustomFields)
			data.Revision = authorization.CustomerRevision(current)
		}
	}
	w.Header().Set("X-FreeFSM-Form", "review")
	w.WriteHeader(code)
	templates.CustomerForm(data).Render(r.Context(), w)
}

func (h *CustomerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if request, ok := authorization.FromContext(r.Context()); ok {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err := request.Service.ArchiveCustomer(r.Context(), request.Actor, id, r.FormValue("archive_snapshot")); err != nil {
			authorizationError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/customers/%d", id), 303)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	c, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := c.DisplayName
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	credit, queryErr := h.settlement.CustomerSettlement(r.Context(), actor, id)
	if queryErr != nil {
		writeSettlementError(w, r, queryErr)
		return
	}
	if credit.AvailableCreditCents > 0 {
		http.Error(w, "customer cannot be archived while credit remains unresolved", http.StatusConflict)
		return
	}
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/customers?flash=Customer+archived", http.StatusSeeOther)
}

func (h *CustomerHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	c, err := h.svc.GetByID(r.Context(), id)
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
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"entity_name": c.DisplayName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/customers/"+strconv.FormatInt(id, 10)+"?flash=Customer+restored", http.StatusSeeOther)
}

func customerToDetail(ctx context.Context, c *ent.Customer) templates.CustomerDetail {
	d := templates.CustomerDetail{
		ID:              c.ID,
		FirstName:       c.FirstName,
		LastName:        c.LastName,
		DisplayName:     c.DisplayName,
		Email:           c.Email,
		Phone:           c.Phone,
		CompanyName:     c.CompanyName,
		Notes:           c.Notes,
		ServiceNotes:    c.ServiceNotes,
		Status:          c.Status,
		AccountType:     c.AccountType,
		BillingAddress1: c.BillingAddress1,
		BillingAddress2: c.BillingAddress2,
		BillingCity:     c.BillingCity,
		BillingState:    c.BillingState,
		BillingZipCode:  c.BillingZipCode,
		TaxExempt:       c.TaxExempt,
	}
	if c.DeletedAt != nil && !c.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *c.DeletedAt)
	}
	return d
}

func customerRow(c *ent.Customer) templates.CustomerRow {
	return templates.CustomerRow{
		ID:          c.ID,
		DisplayName: c.DisplayName,
		FirstName:   c.FirstName,
		LastName:    c.LastName,
		Email:       c.Email,
		Phone:       c.Phone,
		CompanyName: c.CompanyName,
		Status:      c.Status,
		AccountType: c.AccountType,
	}
}

func formDataFromCustomer(ctx context.Context, c *ent.Customer) templates.CustomerFormPageData {
	d := customerToDetail(ctx, c)
	return templates.CustomerFormPageData{
		Customer:     &d,
		IsNew:        false,
		Statuses:     services.CustomerStatuses,
		AccountTypes: services.CustomerAccountTypes,
	}
}

func (h *CustomerHandler) Contacts(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	contacts, _ := h.contactSvc.ListByCustomer(r.Context(), id)
	opts := make([]templates.SelectOption, len(contacts))
	for i, c := range contacts {
		label := c.FirstName + " " + c.LastName
		opts[i] = templates.SelectOption{Value: c.ID, Label: label}
	}
	selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
	templates.ContactOptions(opts, selected).Render(r.Context(), w)
}

func (h *CustomerHandler) ListContacts(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	contacts, _ := h.contactSvc.ListByCustomer(r.Context(), id)
	rows := make([]templates.ContactRow, len(contacts))
	for i, c := range contacts {
		rows[i] = templates.ContactRow{
			ID: c.ID, FirstName: c.FirstName, LastName: c.LastName,
			Email: c.Email, Phone: c.Phone,
		}
	}
	if customerEditRailCompact(r) {
		templates.ContactsListCompact(rows, id, r.URL.Query().Get("read_only") == "1").Render(r.Context(), w)
		return
	}
	templates.ContactsList(rows, id, r.URL.Query().Get("read_only") == "1").Render(r.Context(), w)
}

func (h *CustomerHandler) LocationOptions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	locations, _ := h.locationSvc.ListByCustomer(r.Context(), id)
	selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
	templates.LocationOptions(locationRowsToOptions(locations), selected).Render(r.Context(), w)
}

func (h *CustomerHandler) ListLocations(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	locations, _ := h.locationSvc.ListByCustomer(r.Context(), id)
	if customerEditRailCompact(r) {
		templates.LocationsListCompact(locationRows(locations), id, r.URL.Query().Get("read_only") == "1").Render(r.Context(), w)
		return
	}
	templates.LocationsList(locationRows(locations), id, r.URL.Query().Get("read_only") == "1").Render(r.Context(), w)
}

func customerEditRailCompact(r *http.Request) bool {
	return r.URL.Query().Get("compact") == "1" || r.FormValue("compact") == "1"
}

func (h *CustomerHandler) canReadCustomer(r *http.Request, customerID int64) bool {
	u, ok := middleware.UserFromContext(r.Context())
	return ok && u != nil && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeCustomer, customerID), policyRead)
}

func (h *CustomerHandler) authorizeCustomerUpdate(w http.ResponseWriter, r *http.Request, customerID int64) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeCustomer, customerID), policyUpdate) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (h *CustomerHandler) NewContactForm(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	if customerEditRailCompact(r) {
		templates.ContactFormCompact(id).Render(r.Context(), w)
		return
	}
	templates.ContactForm(id).Render(r.Context(), w)
}

func (h *CustomerHandler) NewLocationForm(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	if customerEditRailCompact(r) {
		templates.LocationFormCompact(id).Render(r.Context(), w)
		return
	}
	templates.LocationForm(id).Render(r.Context(), w)
}

func (h *CustomerHandler) CreateLocation(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	r.ParseForm()
	u, _ := middleware.UserFromContext(r.Context())
	l, err := h.locationSvc.CreateForCustomer(r.Context(), u.CompanyID, id, services.CustomerLocationCreateParams{
		Title:     r.FormValue("title"),
		Address1:  r.FormValue("address_1"),
		Address2:  r.FormValue("address_2"),
		City:      r.FormValue("city"),
		State:     r.FormValue("state"),
		ZipCode:   r.FormValue("zip_code"),
		Notes:     r.FormValue("notes"),
		IsPrimary: r.FormValue("is_primary") == "on",
	})
	if err != nil {
		internalServerError(w, r, "create customer location", err)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "location_created", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": l.Title,
		})
	}
	h.ListLocations(w, r)
}

func (h *CustomerHandler) CreateLocationInline(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	l, err := h.locationSvc.CreateForCustomer(r.Context(), u.CompanyID, id, services.CustomerLocationCreateParams{
		Title:     title,
		Address1:  r.FormValue("address_1"),
		Address2:  r.FormValue("address_2"),
		City:      r.FormValue("city"),
		State:     r.FormValue("state"),
		ZipCode:   r.FormValue("zip_code"),
		Notes:     r.FormValue("notes"),
		IsPrimary: r.FormValue("is_primary") == "true",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "location_created", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": l.Title,
		})
	}
	writeInlineOptionJSON(w, l.ID, l.Title)
}

func (h *CustomerHandler) EditLocationForm(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	lid, _ := strconv.ParseInt(chi.URLParam(r, "lid"), 10, 64)
	l, err := h.locationSvc.GetByCustomer(r.Context(), custID, lid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if customerEditRailCompact(r) {
		templates.LocationEditCard(custID, locationRow(l)).Render(r.Context(), w)
		return
	}
	templates.LocationEditRow(custID, locationRow(l)).Render(r.Context(), w)
}

func (h *CustomerHandler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	lid, _ := strconv.ParseInt(chi.URLParam(r, "lid"), 10, 64)
	r.ParseForm()
	isPrimary := r.FormValue("is_primary") == "on"
	l, err := h.locationSvc.UpdateCustomerLocation(r.Context(), custID, lid, services.CustomerLocationUpdateParams{
		Title:     formPtr(r.FormValue("title")),
		Address1:  formPtr(r.FormValue("address_1")),
		Address2:  formPtr(r.FormValue("address_2")),
		City:      formPtr(r.FormValue("city")),
		State:     formPtr(r.FormValue("state")),
		ZipCode:   formPtr(r.FormValue("zip_code")),
		Notes:     formPtr(r.FormValue("notes")),
		IsPrimary: &isPrimary,
	})
	if err != nil {
		internalServerError(w, r, "update customer location", err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "location_updated", objectref.New(objectref.TypeCustomer, custID), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": l.Title,
		})
	}
	if customerEditRailCompact(r) {
		templates.LocationViewCard(custID, locationRow(l), false).Render(r.Context(), w)
		return
	}
	templates.LocationViewRow(custID, locationRow(l), false).Render(r.Context(), w)
}

func (h *CustomerHandler) DeleteLocation(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	lid, _ := strconv.ParseInt(chi.URLParam(r, "lid"), 10, 64)
	l, err := h.locationSvc.GetByCustomer(r.Context(), custID, lid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "location_deleted", objectref.New(objectref.TypeCustomer, custID), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": l.Title,
		})
	}
	if err := h.locationSvc.DeleteCustomerLocation(r.Context(), custID, lid); err != nil {
		internalServerError(w, r, "delete customer location", err)
		return
	}
	h.ListLocations(w, r)
}

func (h *CustomerHandler) CreateContact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	r.ParseForm()
	u, _ := middleware.UserFromContext(r.Context())
	_, err := h.contactSvc.Create(r.Context(), u.CompanyID, id, services.ContactCreateParams{
		FirstName: r.FormValue("first_name"),
		LastName:  r.FormValue("last_name"),
		Email:     r.FormValue("email"),
		Phone:     r.FormValue("phone"),
		Notes:     r.FormValue("notes"),
	})
	if err != nil {
		internalServerError(w, r, "create customer contact", err)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "contact_created", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": r.FormValue("first_name") + " " + r.FormValue("last_name"),
		})
	}
	h.ListContacts(w, r)
}

func (h *CustomerHandler) CreateContactInline(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, id) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	firstName := strings.TrimSpace(r.FormValue("first_name"))
	if firstName == "" {
		http.Error(w, "first name is required", http.StatusBadRequest)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	c, err := h.contactSvc.Create(r.Context(), u.CompanyID, id, services.ContactCreateParams{
		FirstName: firstName,
		LastName:  r.FormValue("last_name"),
		Email:     r.FormValue("email"),
		Phone:     r.FormValue("phone"),
		Notes:     r.FormValue("notes"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	label := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "contact_created", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": label,
		})
	}
	writeInlineOptionJSON(w, c.ID, label)
}

func (h *CustomerHandler) EditContactForm(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	c, err := h.contactSvc.GetByCustomer(r.Context(), custID, cid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if customerEditRailCompact(r) {
		templates.ContactEditCard(custID, cid, c.FirstName, c.LastName, c.Email, c.Phone, c.Notes).Render(r.Context(), w)
		return
	}
	templates.ContactEditRow(custID, cid, c.FirstName, c.LastName, c.Email, c.Phone, c.Notes).Render(r.Context(), w)
}

func (h *CustomerHandler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if _, err := h.contactSvc.GetByCustomer(r.Context(), custID, cid); err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	c, err := h.contactSvc.Update(r.Context(), cid, contactUpdateParamsFromRequest(r))
	if err != nil {
		internalServerError(w, r, "update customer contact", err)
		return
	}
	row := templates.ContactRow{
		ID: c.ID, FirstName: c.FirstName, LastName: c.LastName,
		Email: c.Email, Phone: c.Phone,
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "contact_updated", objectref.New(objectref.TypeCustomer, custID), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": c.FirstName + " " + c.LastName,
		})
	}
	if customerEditRailCompact(r) {
		templates.ContactViewCard(custID, row, false).Render(r.Context(), w)
		return
	}
	templates.ContactViewRow(custID, row).Render(r.Context(), w)
}

func contactUpdateParamsFromRequest(r *http.Request) services.ContactUpdateParams {
	return services.ContactUpdateParams{
		FirstName: formPtr(r.FormValue("first_name")),
		LastName:  strPtr(r.FormValue("last_name")),
		Email:     strPtr(r.FormValue("email")),
		Phone:     strPtr(r.FormValue("phone")),
		Notes:     strPtr(r.FormValue("notes")),
	}
}

func (h *CustomerHandler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !h.authorizeCustomerUpdate(w, r, custID) {
		return
	}
	contact, err := h.contactSvc.GetByCustomer(r.Context(), custID, cid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "contact_deleted", objectref.New(objectref.TypeCustomer, custID), map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": contact.FirstName + " " + contact.LastName,
		})
	}
	if err := h.contactSvc.Delete(r.Context(), cid); err != nil {
		internalServerError(w, r, "delete customer contact", err)
		return
	}
	h.ListContacts(w, r)
}

func (h *CustomerHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeCustomer, id))
	if err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeCustomer, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/customers/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *CustomerHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeCustomer, id)); err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeCustomer, id), map[string]interface{}{
		"actor_name": u.Name,
		"tag_name":   tag.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeCustomer, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/customers/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func newFormData() templates.CustomerFormPageData {
	return templates.CustomerFormPageData{
		Customer: &templates.CustomerDetail{
			Status:      "lead",
			AccountType: "individual",
		},
		IsNew:        true,
		Statuses:     services.CustomerStatuses,
		AccountTypes: services.CustomerAccountTypes,
	}
}

func initializeCustomerErrorIdentity(data *templates.CustomerFormPageData, r *http.Request, id int64) {
	data.IsNew = id == 0
	data.OperationID = newOperationKey()
	// Retained inputs belong to the submitted revision. Only an explicit
	// conflict comparison may offer the current record's revision for review.
	data.Revision = r.FormValue("revision")
}

func (h *CustomerHandler) fieldError(w http.ResponseWriter, r *http.Request, id int64, err error) bool {
	var fieldErr *customfields.FieldError
	if !errors.As(err, &fieldErr) {
		return false
	}
	d := newFormData()
	d.IsNew = id == 0
	permission := "customer.edit"
	if id == 0 {
		permission = "customer.create"
	}
	if !customerPermission(r, permission) {
		authorizationError(w, r, authorization.ErrForbidden)
		return true
	}
	old := "[]"
	if id != 0 {
		c, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return true
		}
		d = formDataFromCustomer(r.Context(), c)
		old = c.CustomFields
	}
	fields := map[string]*string{"display_name": &d.Customer.DisplayName, "first_name": &d.Customer.FirstName, "last_name": &d.Customer.LastName, "email": &d.Customer.Email, "phone": &d.Customer.Phone, "company_name": &d.Customer.CompanyName, "service_notes": &d.Customer.ServiceNotes, "notes": &d.Customer.Notes, "account_type": &d.Customer.AccountType, "status": &d.Customer.Status}
	for name, target := range fields {
		if r.PostForm.Has(name) {
			*target = r.PostForm.Get(name)
		}
	}
	if customerPermission(r, "customer.financial.view") {
		for name, target := range map[string]*string{"billing_address_1": &d.Customer.BillingAddress1, "billing_address_2": &d.Customer.BillingAddress2, "billing_city": &d.Customer.BillingCity, "billing_state": &d.Customer.BillingState, "billing_zip_code": &d.Customer.BillingZipCode} {
			if r.PostForm.Has(name) {
				*target = r.PostForm.Get(name)
			}
		}
		if r.PostForm.Has("tax_exempt") || r.PostForm.Has("tax_exempt_present") {
			d.Customer.TaxExempt = r.PostForm.Get("tax_exempt") == "true"
		}
	}
	defs, loadErr := h.defSvc.ListForObjectType(r.Context(), "customer")
	if loadErr != nil {
		http.Error(w, "Unable to load fields", 500)
		return true
	}
	d.CustomFields = buildCustomFieldErrorDisplay(defs, old, parseCustomFieldValues(r), id == 0)
	d.Errors = map[string]string{"custom_fields": fieldErr.Error()}
	initializeCustomerErrorIdentity(&d, r, id)
	w.WriteHeader(http.StatusUnprocessableEntity)
	templates.CustomerForm(d).Render(r.Context(), w)
	return true
}

func filesToRows(ctx context.Context, files []*ent.File) []templates.FileRow {
	rows := make([]templates.FileRow, len(files))
	for i, f := range files {
		rows[i] = templates.FileRow{
			ID:           f.ID,
			Revision:     f.Revision,
			OriginalName: f.OriginalName,
			MimeType:     f.MimeType,
			FileSize:     services.FormatFileSize(f.FileSize),
			CreatedAt:    displayDateTime(ctx, f.CreatedAt),
		}
		if request, ok := authorization.FromContext(ctx); ok {
			ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
			if err == nil {
				rows[i].CanDownload = request.Service.FileAccess(ctx, request.Actor, ref, "file.download", f.UploadedBy) == nil
				rows[i].CanRename = request.Service.FileAccess(ctx, request.Actor, ref, "file.rename", f.UploadedBy) == nil
				rows[i].CanRemove = request.Service.FileAccess(ctx, request.Actor, ref, "file.remove", f.UploadedBy) == nil
			}
		}
	}
	return rows
}

func contactRows(contacts []*ent.CustomerContact) []templates.ContactRow {
	rows := make([]templates.ContactRow, len(contacts))
	for i, c := range contacts {
		rows[i] = templates.ContactRow{ID: c.ID, FirstName: c.FirstName, LastName: c.LastName, Email: c.Email, Phone: c.Phone}
	}
	return rows
}

func (h *CustomerHandler) statusesByType(ctx context.Context, objectType string) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, objectType)
	return statuses
}

func (h *CustomerHandler) customerJobRows(ctx context.Context, jobs []*ent.Job) []templates.JobRow {
	statuses := h.statusesByType(ctx, "job")
	rows := make([]templates.JobRow, len(jobs))
	for i, j := range jobs {
		rows[i] = jobRow(ctx, j, statuses, map[int64]string{j.CustomerID: ""})
	}
	return rows
}

func (h *CustomerHandler) customerEstimateRows(ctx context.Context, estimates []*ent.Estimate) []templates.EstimateRow {
	statuses := h.statusesByType(ctx, "estimate")
	rows := make([]templates.EstimateRow, len(estimates))
	for i, e := range estimates {
		rows[i] = estimateRow(ctx, e, statuses, map[int64]string{estCustID(e): ""})
	}
	return rows
}

func (h *CustomerHandler) customerInvoiceRows(ctx context.Context, invoices []*ent.Invoice) []templates.InvoiceRow {
	statuses := h.statusesByType(ctx, "invoice")
	rows := make([]templates.InvoiceRow, len(invoices))
	for i, inv := range invoices {
		rows[i] = invoiceRow(ctx, h.effectiveInvoice(ctx, inv), statuses, map[int64]string{invCustID(inv): ""})
	}
	return rows
}

func (h *CustomerHandler) effectiveInvoice(ctx context.Context, invoice *ent.Invoice) *ent.Invoice {
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

func addInvoiceFinancial(summary *templates.CustomerFinancialSummary, inv *ent.Invoice, view settlement.InvoiceSettlement, invoiceStatuses []*ent.Status, loc *time.Location) {
	today := time.Now()
	category := statusCategory(invoiceStatuses, inv.StatusID)
	if category != "invoice:void" {
		summary.TotalInvoiced += float64(view.TotalCents) / 100
		summary.TotalPaid += float64(view.SettledCents) / 100
	}
	if category == "invoice:draft" || category == "invoice:void" || view.AmountDueCents <= 0 {
		return
	}
	balance := float64(view.AmountDueCents) / 100
	summary.TotalBalance += balance
	summary.OpenInvoiceCount++
	if services.IsCompanyDateOverdue(inv.DueDate, today, loc) {
		summary.OverdueBalance += balance
		summary.OverdueInvoiceCount++
	}
}

func customerFinancialSummary(ctx context.Context, svc settlementService, actor settlement.Actor, invoices []*ent.Invoice, statuses []*ent.Status, loc *time.Location) (templates.CustomerFinancialSummary, error) {
	var summary templates.CustomerFinancialSummary
	for _, inv := range invoices {
		view, err := svc.InvoiceSettlement(ctx, actor, inv.ID)
		if err != nil {
			return templates.CustomerFinancialSummary{}, err
		}
		addInvoiceFinancial(&summary, inv, view, statuses, loc)
	}
	finishCustomerFinancial(&summary)
	return summary, nil
}

func finishCustomerFinancial(summary *templates.CustomerFinancialSummary) {
	summary.CurrentBalance = summary.TotalBalance - summary.OverdueBalance
	if summary.CurrentBalance < 0 {
		summary.CurrentBalance = 0
	}
	if summary.TotalInvoiced > 0 {
		summary.PaidPercent = int((summary.TotalPaid / summary.TotalInvoiced) * 100)
		summary.OpenPercent = int((summary.CurrentBalance / summary.TotalInvoiced) * 100)
		summary.OverduePercent = int((summary.OverdueBalance / summary.TotalInvoiced) * 100)
		if summary.PaidPercent > 100 {
			summary.PaidPercent = 100
		}
		if summary.OpenPercent > 100 {
			summary.OpenPercent = 100
		}
		if summary.OverduePercent > 100 {
			summary.OverduePercent = 100
		}
	}
}

func (h *CustomerHandler) RefundCredit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err = uniqueSettlementForm(r); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	amount, err := parseCents(r.FormValue("amount"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	_, err = h.settlement.RefundCredit(r.Context(), actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, CustomerID: id, AmountCents: amount, Method: settlement.PaymentMethod(r.FormValue("method")), EffectiveDate: settlement.Date(r.FormValue("effective_date")), Reference: r.FormValue("reference"), Notes: r.FormValue("notes"), Reason: r.FormValue("reason")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		refundOperationResponse(w, r, r.FormValue("idempotency_key"))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/customers/%d?flash=Customer+credit+refunded", id), 303)
}

func (h *CustomerHandler) ReverseRefund(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err = uniqueSettlementForm(r); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	refundID, err := parseUUID(chi.URLParam(r, "refund_id"), "refund")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	actor, ok := settlementActor(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	_, err = h.settlement.ReverseRefund(r.Context(), actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, ID: refundID, CustomerID: id, EffectiveDate: settlement.Date(r.FormValue("effective_date")), Reason: r.FormValue("reason")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		refundOperationResponse(w, r, r.FormValue("idempotency_key"))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/customers/%d?flash=Credit+refund+reversed", id), 303)
}

func locationRows(locations []*ent.Location) []templates.LocationRow {
	rows := make([]templates.LocationRow, len(locations))
	for i, l := range locations {
		rows[i] = locationRow(l)
	}
	return rows
}

func locationRow(l *ent.Location) templates.LocationRow {
	return templates.LocationRow{
		ID:        l.ID,
		Title:     l.Title,
		Address1:  l.Address1,
		Address2:  l.Address2,
		City:      l.City,
		State:     l.State,
		ZipCode:   l.ZipCode,
		Notes:     l.Notes,
		IsPrimary: l.IsPrimary,
	}
}

func locationRowsToOptions(locations []*ent.Location) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(locations))
	for i, l := range locations {
		opts[i] = templates.SelectOption{Value: l.ID, Label: l.Title}
	}
	return opts
}
