package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type LeaseHandler struct {
	authority *authorization.Service
	files     *services.FileService
	tags      *services.TagService
	tagLinks  *services.TagLinkService
	fields    *services.CustomFieldDefinitionService
}

func (h *LeaseHandler) Finalize(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	details, e1 := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	billing, e2 := strconv.ParseInt(r.FormValue("billing_revision"), 10, 64)
	if e1 != nil || e2 != nil {
		http.Error(w, "Review current Lease and Billing revisions", 400)
		return
	}
	err = h.authority.FinalizeLease(r.Context(), authorizationActor(r), id, authorization.LeaseFinalizationInput{DetailsRevision: details, BillingRevision: billing, ManagedBillingStart: r.FormValue("managed_billing_start")})
	if err != nil {
		var validation *authorization.LeaseValidationError
		if errors.As(err, &validation) {
			http.Error(w, validation.Message, http.StatusConflict)
			return
		}
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/leases/%d", id), http.StatusSeeOther)
}

func (h *LeaseHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	data, err := h.authority.LeaseList(r.Context(), authorizationActor(r), r.URL.Query().Get("facet") == "billing", r.URL.Query().Get("search"), r.URL.Query().Get("state"), page, size)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	templates.LeaseIndex(data).Render(r.Context(), w)
}
func (h *LeaseHandler) New(w http.ResponseWriter, r *http.Request) {
	choices, err := h.authority.LeaseRegistrationCustomers(r.Context(), authorizationActor(r))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	templates.LeaseRegistrationForm(choices).Render(r.Context(), w)
}
func (h *LeaseHandler) History(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := h.authority.LeaseHistory(r.Context(), authorizationActor(r), id, strings.Contains(r.URL.Path, "/billing/"))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	templates.LeaseHistoryPage(data).Render(r.Context(), w)
}

func (h *LeaseHandler) Archive(w http.ResponseWriter, r *http.Request) { h.lifecycle(w, r, false) }
func (h *LeaseHandler) Restore(w http.ResponseWriter, r *http.Request) { h.lifecycle(w, r, true) }
func (h *LeaseHandler) lifecycle(w http.ResponseWriter, r *http.Request, restore bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.NotFound(w, r)
		return
	}
	if err = h.authority.LeaseLifecycle(r.Context(), authorizationActor(r), id, restore); err != nil {
		authorizationError(w, r, err)
		return
	}
	destination := fmt.Sprintf("/leases/%d", id)
	if !restore {
		p, err := h.authority.Principal(r.Context(), authorizationActor(r))
		if err != nil || !p.Allows("lease.view", authorization.All) || !p.Allows("lease.archived.view", authorization.All) {
			destination = "/leases"
		}
	}
	http.Redirect(w, r, destination, 303)
}

func (h *LeaseHandler) Register(w http.ResponseWriter, r *http.Request) {
	customer, err := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if err != nil || customer < 1 {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Select an active Customer", http.StatusBadRequest)
		return
	}
	lease, err := h.authority.RegisterDraftLease(r.Context(), authorizationActor(r), customer)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/leases/%d", lease.ID), http.StatusSeeOther)
}

func (h *LeaseHandler) Show(w http.ResponseWriter, r *http.Request) {
	h.show(w, r, nil, "")
}

func (h *LeaseHandler) show(w http.ResponseWriter, r *http.Request, submitted *authorization.DraftLeaseDetailsInput, message string) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lease, err := h.authority.DraftLeaseBase(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	choices, err := h.authority.DraftLeaseChoices(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	ref := objectref.New(objectref.TypeLease, id)
	files, err := h.files.List(r.Context(), u.CompanyID, ref)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	tags, err := h.tagLinks.ListForObject(r.Context(), u.CompanyID, ref)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	defs, err := h.fields.ListForObjectType(r.Context(), "lease")
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	data := templates.LeasePageData{Base: lease, Choices: choices, Tags: tagsToRows(tags), Fields: buildCustomFieldDisplay(defs, lease.CustomFields), Files: templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "lease"}}
	if submitted != nil {
		if !lease.CanManage {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		if err := h.authority.ParentAccess(r.Context(), authorizationActor(r), "lease", id, "lease.manage"); err != nil {
			authorizationError(w, r, err)
			return
		}
		if submitted.AgreementFileID != nil {
			// Preserve replace/clear separately from omission, using only the
			// current exact-parent File list and independently current authority.
			// An unavailable choice rejects recovery; never fall back to A/None.
			if !data.Choices.CanSelectAgreement {
				authorizationError(w, r, authorization.ErrForbidden)
				return
			}
			if err := h.authority.FileAccess(r.Context(), authorizationActor(r), ref, "file.download", 0); err != nil {
				authorizationError(w, r, err)
				return
			}
			if selected := *submitted.AgreementFileID; selected != nil {
				allowed := false
				for _, file := range data.Files.Files {
					if file.ID == *selected && file.CanDownload {
						allowed = true
						break
					}
				}
				if !allowed {
					authorizationError(w, r, authorization.ErrNotFound)
					return
				}
			}
			data.Base.AgreementFileID = *submitted.AgreementFileID
		}
		data.Error = message
		data.Base.Revision = submitted.Revision
		data.Base.Title, data.Base.Reference = submitted.Title, submitted.Reference
		data.Base.ExecutionDate, data.Base.StartsOn, data.Base.EndsOn = submitted.ExecutionDate, submitted.StartsOn, submitted.EndsOn
		data.Base.PremisesID, data.Base.AssetIDs = submitted.PremisesID, submitted.AssetIDs
		if submitted.Fields != nil {
			data.Fields = buildCustomFieldErrorDisplay(defs, lease.CustomFields, *submitted.Fields, false)
		}
	}
	if canAttachTag(r, "lease", id) {
		tags, err := h.tags.ListAll(r.Context(), u.CompanyID)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		data.AllTags = tagsToRows(tags)
	}
	if submitted != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	templates.LeaseDetailsPage(data).Render(r.Context(), w)
}

func leaseOptionalID(r *http.Request, name string) (*int64, error) {
	value := r.FormValue(name)
	if value == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return nil, authorization.ErrConflict
	}
	return &id, nil
}

func (h *LeaseHandler) Billing(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		p, err := h.authority.DraftLeaseBilling(r.Context(), authorizationActor(r), id)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		templates.LeaseBillingPage(p).Render(r.Context(), w)
		return
	}
	// Authorize before parsing sensitive input; validation errors never reflect it.
	if _, err := h.authority.DraftLeaseBilling(r.Context(), authorizationActor(r), id); err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Reload current Billing terms", 409)
		return
	}
	asset, err := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Select an intended Asset", 400)
		return
	}
	term := authorization.DraftLeaseTerms{AssetID: asset, TaxRate: r.FormValue("tax_rate")}
	for name, dst := range map[string]**int64{"initial_cents": &term.InitialCents, "rent_cents": &term.RentCents} {
		if raw := r.FormValue(name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				saveaction.RejectBeforeWrite(r.Context())
				http.Error(w, "Enter nonnegative whole cents", 400)
				return
			}
			*dst = &value
		}
	}
	var lifecycleRevision int64
	if raw := r.FormValue("lifecycle_revision"); raw != "" {
		lifecycleRevision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			saveaction.RejectBeforeWrite(r.Context())
			http.Error(w, "Reload current Billing terms", 409)
			return
		}
	}
	if err := h.authority.SaveDraftLeaseBilling(r.Context(), authorizationActor(r), id, revision, r.FormValue("initial_due_on"), term, lifecycleRevision); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/leases/%d/billing", id), 303)
}

func (h *LeaseHandler) Save(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.NotFound(w, r)
		return
	}
	if err = r.ParseForm(); err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Invalid form", 400)
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Reload the current Draft before saving", 409)
		return
	}
	premises, err := leaseOptionalID(r, "premises_id")
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		authorizationError(w, r, err)
		return
	}
	in := authorization.DraftLeaseDetailsInput{Revision: revision, Title: r.FormValue("title"), Reference: r.FormValue("reference"), ExecutionDate: r.FormValue("execution_date"), StartsOn: r.FormValue("starts_on"), EndsOn: r.FormValue("ends_on"), PremisesID: premises, Fields: parseCustomFieldPatch(r), AssetIDs: []int64{}}
	for _, value := range r.Form["asset_ids"] {
		if value == "" {
			continue
		}
		asset, err := strconv.ParseInt(value, 10, 64)
		if err != nil || asset < 1 {
			saveaction.RejectBeforeWrite(r.Context())
			http.Error(w, "Invalid intended Asset", 400)
			return
		}
		in.AssetIDs = append(in.AssetIDs, asset)
	}
	if r.Form.Has("agreement_file_id") {
		file, err := leaseOptionalID(r, "agreement_file_id")
		if err != nil {
			saveaction.RejectBeforeWrite(r.Context())
			authorizationError(w, r, err)
			return
		}
		in.AgreementFileID = &file
	}
	if err = h.authority.SaveDraftLeaseDetails(r.Context(), authorizationActor(r), id, in); err != nil {
		var fieldErr *customfields.FieldError
		if errors.As(err, &fieldErr) {
			h.show(w, r, &in, fieldErr.Error())
			return
		}
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/leases/%d", id), 303)
}
