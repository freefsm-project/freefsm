package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CustomerHandler struct {
	svc         *services.CustomerService
	contactSvc  *services.CustomerContactService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	fileSvc     *services.FileService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
}

func NewCustomerHandler(svc *services.CustomerService, contactSvc *services.CustomerContactService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, fileSvc *services.FileService, activitySvc *services.ActivityService, policySvc *services.PolicyService) *CustomerHandler {
	return &CustomerHandler{svc: svc, contactSvc: contactSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, fileSvc: fileSvc, activitySvc: activitySvc, policySvc: policySvc}
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
	c, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !h.canReadCustomer(r, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "customer", c.ID)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
	files, _ := h.fileSvc.List(r.Context(), "customer", c.ID)
	ctx := middleware.WithPageHeaderTitle(r.Context(), c.DisplayName)
	templates.CustomerShow(templates.CustomerShowPageData{
		Customer:     customerToDetail(c),
		Tags:         tagsToRows(tags),
		AllTags:      tagsToRows(allTags),
		CustomFields: buildCustomFieldDisplay(defs, c.CustomFields),
		FileList:     templates.FileListPageData{Files: filesToRows(files), ObjectID: c.ID, ObjectType: "customer"},
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
		ServiceAddress1: r.FormValue("service_address_1"),
		ServiceAddress2: r.FormValue("service_address_2"),
		ServiceCity:     r.FormValue("service_city"),
		ServiceState:    r.FormValue("service_state"),
		ServiceZipCode:  r.FormValue("service_zip_code"),
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
	http.Redirect(w, r, "/customers?flash=Customer+created", http.StatusSeeOther)
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
		data := formDataFromCustomer(c)
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "customer")
		data.CustomFields = buildCustomFieldDisplay(defs, c.CustomFields)
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
		ServiceAddress1: formPtr(r.FormValue("service_address_1")),
		ServiceAddress2: formPtr(r.FormValue("service_address_2")),
		ServiceCity:     formPtr(r.FormValue("service_city")),
		ServiceState:    formPtr(r.FormValue("service_state")),
		ServiceZipCode:  formPtr(r.FormValue("service_zip_code")),
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

func customerToDetail(c *ent.Customer) templates.CustomerDetail {
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
		ServiceAddress1: c.ServiceAddress1,
		ServiceAddress2: c.ServiceAddress2,
		ServiceCity:     c.ServiceCity,
		ServiceState:    c.ServiceState,
		ServiceZipCode:  c.ServiceZipCode,
	}
	if c.DeletedAt != nil && !c.DeletedAt.IsZero() {
		d.ArchivedAt = c.DeletedAt.Format("Jan 2, 2006")
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

func formDataFromCustomer(c *ent.Customer) templates.CustomerFormPageData {
	d := customerToDetail(c)
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
	templates.ContactsList(rows, id).Render(r.Context(), w)
}

func (h *CustomerHandler) canReadCustomer(r *http.Request, customerID int64) bool {
	u, ok := middleware.UserFromContext(r.Context())
	return ok && u != nil && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "customer", customerID, policyRead)
}

func (h *CustomerHandler) NewContactForm(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	templates.ContactForm(id).Render(r.Context(), w)
}

func (h *CustomerHandler) CreateContact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	r.ParseForm()
	_, err := h.contactSvc.Create(r.Context(), id, services.ContactCreateParams{
		FirstName: r.FormValue("first_name"),
		LastName:  r.FormValue("last_name"),
		Email:     r.FormValue("email"),
		Phone:     r.FormValue("phone"),
		Notes:     r.FormValue("notes"),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
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
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	contacts, _ := h.contactSvc.ListByCustomer(r.Context(), custID)
	for _, c := range contacts {
		if c.ID == cid {
			templates.ContactEditRow(custID, cid, c.FirstName, c.LastName, c.Email, c.Phone, c.Notes).Render(r.Context(), w)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *CustomerHandler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	r.ParseForm()
	c, err := h.contactSvc.Update(r.Context(), cid, services.ContactUpdateParams{
		FirstName: formPtr(r.FormValue("first_name")),
		LastName:  formPtr(r.FormValue("last_name")),
		Email:     formPtr(r.FormValue("email")),
		Phone:     formPtr(r.FormValue("phone")),
		Notes:     formPtr(r.FormValue("notes")),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
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
	templates.ContactViewRow(custID, row).Render(r.Context(), w)
}

func (h *CustomerHandler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	custID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	contact, err := h.contactSvc.GetByID(r.Context(), cid)
	if err == nil {
		u, _ := middleware.UserFromContext(r.Context())
		if u != nil {
			h.activitySvc.Record(r.Context(), u.ID, "contact_deleted", "customer", custID, map[string]interface{}{
				"actor_name":  u.Name,
				"entity_name": contact.FirstName + " " + contact.LastName,
			})
		}
	}
	h.contactSvc.Delete(r.Context(), cid)
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

func filesToRows(files []*ent.File) []templates.FileRow {
	rows := make([]templates.FileRow, len(files))
	for i, f := range files {
		rows[i] = templates.FileRow{
			ID:           f.ID,
			OriginalName: f.OriginalName,
			MimeType:     f.MimeType,
			FileSize:     services.FormatFileSize(f.FileSize),
			CreatedAt:    f.CreatedAt.Format("2006-01-02 15:04"),
		}
	}
	return rows
}
