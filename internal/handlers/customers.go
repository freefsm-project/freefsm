package handlers

import (
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CustomerHandler struct {
	svc        *services.CustomerService
	contactSvc *services.CustomerContactService
}

func NewCustomerHandler(svc *services.CustomerService, contactSvc *services.CustomerContactService) *CustomerHandler {
	return &CustomerHandler{svc: svc, contactSvc: contactSvc}
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

	if r.Header.Get("HX-Request") == "true" {
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
	templates.CustomerShow(templates.CustomerShowPageData{
		Customer: customerToDetail(c),
	}).Render(r.Context(), w)
}

func (h *CustomerHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.CustomerForm(newFormData()).Render(r.Context(), w)
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
	}
	if params.Status == "" {
		params.Status = "lead"
	}
	if params.AccountType == "" {
		params.AccountType = "individual"
	}
	_, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
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
		templates.CustomerForm(formDataFromCustomer(c)).Render(r.Context(), w)
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
	}
	if _, err := h.svc.Update(r.Context(), id, params); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/customers/"+strconv.FormatInt(id, 10)+"?flash=Customer+updated", http.StatusSeeOther)
}

func (h *CustomerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/customers?flash=Customer+deleted", http.StatusSeeOther)
}

func customerToDetail(c *ent.Customer) templates.CustomerDetail {
	return templates.CustomerDetail{
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
	templates.ContactViewRow(custID, row).Render(r.Context(), w)
}

func (h *CustomerHandler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	cid, _ := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	h.contactSvc.Delete(r.Context(), cid)
	h.ListContacts(w, r)
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
