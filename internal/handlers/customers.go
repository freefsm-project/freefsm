package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
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
}

func NewCustomerHandler(svc *services.CustomerService, contactSvc *services.CustomerContactService, locationSvc *services.LocationService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, activitySvc *services.ActivityService, policySvc *services.PolicyService, jobSvc *services.JobService, estimateSvc *services.EstimateService, invoiceSvc *services.InvoiceService, statusSvc *services.StatusService) *CustomerHandler {
	return &CustomerHandler{svc: svc, contactSvc: contactSvc, locationSvc: locationSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, activitySvc: activitySvc, policySvc: policySvc, jobSvc: jobSvc, estimateSvc: estimateSvc, invoiceSvc: invoiceSvc, statusSvc: statusSvc}
}

func (h *CustomerHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	status := r.URL.Query().Get("status")

	customers, total, err := h.svc.List(r.Context(), search, status, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	rows := make([]templates.CustomerRow, len(customers))
	for i, c := range customers {
		rows[i] = customerRow(c)
	}

	data := templates.CustomerListPageData{
		Customers:    rows,
		Page:         page,
		PerPage:      perPage,
		Total:        total,
		TotalPages:   services.PaginationTotalPages(total, perPage),
		Search:       search,
		StatusFilter: status,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.CustomerTable(data).Render(r.Context(), w)
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
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "customer", c.ID)
	var allTags []*ent.Tag
	u, _ := middleware.UserFromContext(r.Context())
	if isAdminOrDispatcher(u) {
		allTags, _ = h.tagSvc.ListAll(r.Context())
	}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
	files, _ := h.fileSvc.List(r.Context(), "customer", c.ID)
	locations, _ := h.locationSvc.ListByCustomer(r.Context(), c.ID)
	contacts, _ := h.contactSvc.ListByCustomer(r.Context(), c.ID)
	jobs, estimates, invoices := []*ent.Job{}, []*ent.Estimate{}, []*ent.Invoice{}
	var financial templates.CustomerFinancialSummary
	if isAdminOrDispatcher(u) {
		jobs, _ = h.jobSvc.ListByCustomer(r.Context(), c.ID, 10)
		estimates, _ = h.estimateSvc.ListByCustomer(r.Context(), c.ID, 10)
		invoices, _ = h.invoiceSvc.ListByCustomer(r.Context(), c.ID, 10)
		allInvoices, _ := h.invoiceSvc.ListByCustomer(r.Context(), c.ID, 0)
		financial = customerFinancialSummary(allInvoices, h.statusesByType(r.Context(), "invoice"), middleware.CompanyLocation(r.Context()))
	}
	ctx := middleware.WithPageHeaderTitle(r.Context(), c.DisplayName)
	templates.CustomerShow(templates.CustomerShowPageData{
		Customer:     customerToDetail(r.Context(), c),
		Locations:    locationRows(locations),
		Contacts:     contactRows(contacts),
		Jobs:         h.customerJobRows(r.Context(), jobs),
		Estimates:    h.customerEstimateRows(r.Context(), estimates),
		Invoices:     h.customerInvoiceRows(r.Context(), invoices),
		Financial:    financial,
		Tags:         tagsToRows(tags),
		AllTags:      tagsToRows(allTags),
		CustomFields: buildCustomFieldDisplay(defs, c.CustomFields),
		FileList:     templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: c.ID, ObjectType: "customer"},
	}).Render(ctx, w)
}

func (h *CustomerHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data := newFormData()
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
		data.CustomFields = buildCustomFieldDisplay(defs, "[]")
		templates.CustomerForm(data).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
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
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "customer", result.ID, map[string]interface{}{
			"entity_name": result.DisplayName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/customers/%d/edit?flash=%s", result.ID, url.QueryEscape("Customer created. Locations and contacts can now be added.")), http.StatusSeeOther)
}

func (h *CustomerHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
		data.CustomFields = buildCustomFieldDisplay(defs, c.CustomFields)
		locations, _ := h.locationSvc.ListByCustomer(r.Context(), c.ID)
		contacts, _ := h.contactSvc.ListByCustomer(r.Context(), c.ID)
		tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "customer", c.ID)
		allTags, _ := h.tagSvc.ListAll(r.Context())
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
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "customer", id, map[string]interface{}{
			"entity_name": result.DisplayName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/customers/"+strconv.FormatInt(id, 10)+"?flash=Customer+updated", http.StatusSeeOther)
}

func (h *CustomerHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "customer", id, map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.ID, "restored", "customer", id, map[string]interface{}{
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
	return ok && u != nil && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "customer", customerID, policyRead)
}

func (h *CustomerHandler) authorizeCustomerUpdate(w http.ResponseWriter, r *http.Request, customerID int64) bool {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "customer", customerID, policyUpdate) {
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
	l, err := h.locationSvc.CreateForCustomer(r.Context(), id, services.CustomerLocationCreateParams{
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
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "location_created", "customer", id, map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": l.Title,
		})
	}
	h.ListLocations(w, r)
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
		h.activitySvc.Record(r.Context(), u.ID, "location_updated", "customer", custID, map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.ID, "location_deleted", "customer", custID, map[string]interface{}{
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
	_, err := h.contactSvc.Create(r.Context(), id, services.ContactCreateParams{
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
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "contact_created", "customer", id, map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": r.FormValue("first_name") + " " + r.FormValue("last_name"),
		})
	}
	h.ListContacts(w, r)
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
		h.activitySvc.Record(r.Context(), u.ID, "contact_updated", "customer", custID, map[string]interface{}{
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
		h.activitySvc.Record(r.Context(), u.ID, "contact_deleted", "customer", custID, map[string]interface{}{
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
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, "customer", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", "customer", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "customer", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/customers/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *CustomerHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, "customer", id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", "customer", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "customer", id)
	allTags, _ := h.tagSvc.ListAll(r.Context())
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

func filesToRows(ctx context.Context, files []*ent.File) []templates.FileRow {
	rows := make([]templates.FileRow, len(files))
	for i, f := range files {
		rows[i] = templates.FileRow{
			ID:           f.ID,
			OriginalName: f.OriginalName,
			MimeType:     f.MimeType,
			FileSize:     services.FormatFileSize(f.FileSize),
			CreatedAt:    displayDateTime(ctx, f.CreatedAt),
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
		rows[i] = invoiceRow(ctx, inv, statuses, map[int64]string{invCustID(inv): ""})
	}
	return rows
}

func customerFinancialSummary(invoices []*ent.Invoice, invoiceStatuses []*ent.Status, loc *time.Location) templates.CustomerFinancialSummary {
	today := time.Now().In(loc).Truncate(24 * time.Hour)
	var summary templates.CustomerFinancialSummary
	for _, inv := range invoices {
		total, paid, err := services.InvoiceAmountDue(inv)
		if err != nil {
			continue
		}
		status := strings.ToLower(statusName(invoiceStatuses, inv.StatusID))
		if status != "void" {
			summary.TotalInvoiced += total
			summary.TotalPaid += paid
		}
		if status == "draft" || status == "paid" || status == "void" {
			continue
		}
		balance := total - paid
		if balance < 0 {
			balance = 0
		}
		if balance <= 0 {
			continue
		}
		summary.TotalBalance += balance
		summary.OpenInvoiceCount++
		if !inv.DueDate.IsZero() && inv.DueDate.In(loc).Before(today) {
			summary.OverdueBalance += balance
			summary.OverdueInvoiceCount++
		}
	}
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
	return summary
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
