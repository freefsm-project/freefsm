package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type AssetHandler struct {
	svc            *services.AssetService
	assetTypeSvc   *services.AssetTypeService
	assetStatusSvc *services.AssetStatusService
	customerSvc    *services.CustomerService
	locationSvc    *services.LocationService
	tagSvc         *services.TagService
	tagLinkSvc     *services.TagLinkService
	cfSvc          *services.CustomFieldDefinitionService
	fileSvc        *services.FileService
	activitySvc    *services.ActivityService
	policySvc      *services.PolicyService
}

func NewAssetHandler(
	svc *services.AssetService,
	assetTypeSvc *services.AssetTypeService,
	assetStatusSvc *services.AssetStatusService,
	customerSvc *services.CustomerService,
	locationSvc *services.LocationService,
	tagSvc *services.TagService,
	tagLinkSvc *services.TagLinkService,
	cfSvc *services.CustomFieldDefinitionService,
	fileSvc *services.FileService,
	activitySvc *services.ActivityService,
	policySvc *services.PolicyService,
) *AssetHandler {
	return &AssetHandler{
		svc:            svc,
		assetTypeSvc:   assetTypeSvc,
		assetStatusSvc: assetStatusSvc,
		customerSvc:    customerSvc,
		locationSvc:    locationSvc,
		tagSvc:         tagSvc,
		tagLinkSvc:     tagLinkSvc,
		cfSvc:          cfSvc,
		fileSvc:        fileSvc,
		activitySvc:    activitySvc,
		policySvc:      policySvc,
	}
}

func (h *AssetHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	assetTypeID, _ := strconv.ParseInt(r.URL.Query().Get("asset_type_id"), 10, 64)
	assetStatusID, _ := strconv.ParseInt(r.URL.Query().Get("asset_status_id"), 10, 64)

	archived := r.URL.Query().Get("archived") == "1"
	list := h.svc.List
	if archived {
		if !allRecordsPermission(r, "asset.archived.view") {
			authorizationError(w, r, authorization.ErrForbidden)
			return
		}
		list = h.svc.ListArchived
	}
	assets, total, err := list(r.Context(), search, customerID, assetTypeID, assetStatusID, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	assetTypes, _ := h.assetTypeSvc.List(r.Context())
	assetStatuses, _ := h.assetStatusSvc.List(r.Context())

	rows := make([]templates.AssetRow, len(assets))
	for i, a := range assets {
		rows[i] = assetRow(a)
	}

	data := templates.AssetListPageData{
		Archived:      archived,
		Assets:        rows,
		AssetTypes:    assetTypesToOptions(assetTypes),
		AssetStatuses: assetStatusesToOptions(assetStatuses),
		Page:          page,
		PerPage:       perPage,
		Total:         total,
		TotalPages:    services.AssetPaginationTotalPages(total, perPage),
		Search:        search,
		CustomerID:    customerID,
		AssetTypeID:   assetTypeID,
		AssetStatusID: assetStatusID,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.AssetListContent(data).Render(r.Context(), w)
		return
	}
	templates.AssetIndex(data).Render(r.Context(), w)
}

func (h *AssetHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeAsset, id), policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	asset, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	assetTypes, _ := h.assetTypeSvc.List(r.Context())
	assetStatuses, _ := h.assetStatusSvc.List(r.Context())

	// Retain legacy linked work without presenting it as performed service.
	linkedJobs, err := h.svc.LinkedJobs(r.Context(), id)
	if err != nil {
		internalServerError(w, r, "load linked Job history", err)
		return
	}
	linkedJobs = filterReadableJobs(r.Context(), h.policySvc, u, linkedJobs)
	jobRows := make([]templates.JobRow, len(linkedJobs))
	for i, j := range linkedJobs {
		jobRows[i] = jobRowForAsset(j)
	}
	canViewServiceEntries := false
	if req, ok := authorization.FromContext(r.Context()); ok {
		principal, err := req.Service.Principal(r.Context(), req.Actor)
		if err != nil {
			writeServiceEntryError(w, r, err)
			return
		}
		canViewServiceEntries = principal.Allows("service_entry.view", authorization.All)
	}

	// Get tags
	tagLinks, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeAsset, id))
	var allTags []*ent.Tag
	if canAttachTag(r, "asset", id) {
		allTags, _ = h.tagSvc.ListAll(r.Context(), u.CompanyID)
	}
	assignedTags := make([]templates.TagRow, 0, len(tagLinks))
	for _, tl := range tagLinks {
		assignedTags = append(assignedTags, templates.TagRow{
			Retired: tl.Retired,
			ID:      tl.ID,
			Name:    tl.Name,
			Color:   tl.Color,
		})
	}
	availableTags := make([]templates.TagRow, 0)
	for _, t := range allTags {
		found := false
		for _, at := range assignedTags {
			if at.ID == t.ID {
				found = true
				break
			}
		}
		if !found {
			availableTags = append(availableTags, templates.TagRow{
				Retired: t.Retired,
				ID:      t.ID,
				Name:    t.Name,
				Color:   t.Color,
			})
		}
	}

	// Custom fields
	defs, _ := h.cfSvc.ListForObjectType(r.Context(), "asset")
	cfDisplay := buildCustomFieldDisplay(defs, asset.CustomFields)

	assetDetail := assetToDetail(r.Context(), asset, assetTypes, assetStatuses)
	files, _ := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeAsset, id))
	data := templates.AssetShowPageData{
		Asset:                 *assetDetail,
		LinkedJobs:            jobRows,
		CanViewServiceEntries: canViewServiceEntries,
		Tags:                  assignedTags,
		AllTags:               availableTags,
		CustomFields:          cfDisplay,
		FileList:              templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "asset"},
	}

	templates.AssetShow(data).Render(r.Context(), w)
}

func (h *AssetHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		customers, _ := h.customerSvc.ListAll(r.Context())
		assetTypes, _ := h.assetTypeSvc.List(r.Context())
		assetStatuses, _ := h.assetStatusSvc.List(r.Context())
		defs, _ := h.cfSvc.ListForObjectType(r.Context(), "asset")
		templates.AssetForm(templates.AssetFormPageData{
			CustomFields:  buildCustomFieldInitialDisplay(defs),
			IsNew:         true,
			Customers:     customersToOptions(customers),
			AssetTypes:    assetTypesToOptions(assetTypes),
			AssetStatuses: assetStatusesToOptions(assetStatuses),
		}).Render(r.Context(), w)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	if !assetSelectionInput(w, r) {
		return
	}
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	assetTypeID, _ := strconv.ParseInt(r.FormValue("asset_type_id"), 10, 64)
	assetStatusID, _ := strconv.ParseInt(r.FormValue("asset_status_id"), 10, 64)

	var locID, statusID *int64
	if locationID > 0 {
		locID = &locationID
	}
	if assetStatusID > 0 {
		statusID = &assetStatusID
	}

	params := services.AssetCreateParams{
		Number:         r.FormValue("number"),
		OwnershipClass: r.FormValue("ownership_class"),
		CustomerID:     customerID,
		LocationID:     locID,
		AssetTypeID:    assetTypeID,
		AssetStatusID:  statusID,
		Name:           r.FormValue("name"),
		SerialNumber:   r.FormValue("serial_number"),
		Model:          r.FormValue("model"),
		Manufacturer:   r.FormValue("manufacturer"),
		Notes:          r.FormValue("notes"),
		CustomFields:   parseCustomFieldValues(r),
	}
	if !assetDates(w, r, &params.InstalledAt, &params.WarrantyExpires) {
		return
	}

	result, err := h.svc.Create(r.Context(), u.CompanyID, params)
	if err != nil {
		if renderFieldSaveError(w, r, h.cfSvc, "asset", err) {
			return
		}
		writeAssetVocabularyError(w, r, err)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeAsset, result.ID), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/assets?flash=Asset+created", http.StatusSeeOther)
}

func (h *AssetHandler) CreateInline(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !assetSelectionInput(w, r) {
		return
	}
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "asset name is required", http.StatusBadRequest)
		return
	}
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	assetTypeID, _ := strconv.ParseInt(r.FormValue("asset_type_id"), 10, 64)
	if assetTypeID <= 0 {
		http.Error(w, "asset type is required", http.StatusBadRequest)
		return
	}
	if _, err := h.assetTypeSvc.GetByID(r.Context(), assetTypeID); err != nil {
		http.Error(w, "invalid asset type", http.StatusBadRequest)
		return
	}
	assetStatusID, _ := strconv.ParseInt(r.FormValue("asset_status_id"), 10, 64)
	var locID, statusID *int64
	if locationID > 0 {
		locID = &locationID
	}
	if assetStatusID > 0 {
		if _, err := h.assetStatusSvc.GetByID(r.Context(), assetStatusID); err != nil {
			http.Error(w, "invalid asset status", http.StatusBadRequest)
			return
		}
		statusID = &assetStatusID
	}
	var installed, warranty *time.Time
	if !assetDates(w, r, &installed, &warranty) {
		return
	}
	result, err := h.svc.Create(r.Context(), u.CompanyID, services.AssetCreateParams{
		Number:          r.FormValue("number"),
		OwnershipClass:  r.FormValue("ownership_class"),
		CustomerID:      customerID,
		LocationID:      locID,
		AssetTypeID:     assetTypeID,
		AssetStatusID:   statusID,
		Name:            name,
		SerialNumber:    r.FormValue("serial_number"),
		Model:           r.FormValue("model"),
		Manufacturer:    r.FormValue("manufacturer"),
		Notes:           r.FormValue("notes"),
		InstalledAt:     installed,
		WarrantyExpires: warranty,
		CustomFields:    parseCustomFieldValues(r),
	})
	if err != nil {
		if renderFieldSaveError(w, r, h.cfSvc, "asset", err) {
			return
		}
		writeAssetVocabularyError(w, r, err)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeAsset, result.ID), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	writeInlineOptionJSON(w, result.ID, assetLabel(result))
}

func (h *AssetHandler) Update(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		asset, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		customers, _ := h.customerSvc.ListAll(r.Context())
		assetTypes, _ := h.assetTypeSvc.List(r.Context())
		assetStatuses, _ := h.assetStatusSvc.List(r.Context())
		locations, _ := h.locationSvc.ListByCustomer(r.Context(), asset.CustomerID)
		locationOptions := locationsToOptions(locations)
		if asset.LocationID != nil {
			found := false
			for _, option := range locationOptions {
				if option.Value == *asset.LocationID {
					found = true
				}
			}
			if !found {
				locationOptions = append(locationOptions, templates.SelectOption{Value: *asset.LocationID, Label: "Current Location (unavailable for new selection)", Retired: true})
			}
		}
		defs, _ := h.cfSvc.ListForObjectType(r.Context(), "asset")
		templates.AssetForm(templates.AssetFormPageData{
			CustomFields:  buildCustomFieldDisplay(defs, asset.CustomFields),
			IsNew:         false,
			Asset:         assetToDetail(r.Context(), asset, assetTypes, assetStatuses),
			Customers:     customersToOptions(customers),
			Locations:     locationOptions,
			AssetTypes:    assetTypesToOptions(assetTypes),
			AssetStatuses: assetStatusesToOptions(assetStatuses),
		}).Render(r.Context(), w)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	if !assetSelectionInput(w, r) {
		return
	}
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	assetTypeID, _ := strconv.ParseInt(r.FormValue("asset_type_id"), 10, 64)
	var statusID *int64
	// Omitted means unchanged; the form's explicit zero means clear.
	if r.Form.Has("asset_status_id") {
		assetStatusID, err := strconv.ParseInt(r.FormValue("asset_status_id"), 10, 64)
		if err != nil || assetStatusID < 0 {
			http.Error(w, "invalid asset status", http.StatusBadRequest)
			return
		}
		statusID = &assetStatusID
	}

	params := services.AssetUpdateParams{
		ClearInstalledAt:     r.Form.Has("installed_at") && r.FormValue("installed_at") == "",
		ClearWarrantyExpires: r.Form.Has("warranty_expires") && r.FormValue("warranty_expires") == "",
		CustomerID:           &customerID,
		LocationID:           &locationID,
		AssetTypeID:          &assetTypeID,
		AssetStatusID:        statusID,
		Name:                 assetTextPatch(r, "name"),
		SerialNumber:         assetTextPatch(r, "serial_number"),
		Model:                assetTextPatch(r, "model"),
		Manufacturer:         assetTextPatch(r, "manufacturer"),
		Notes:                assetTextPatch(r, "notes"),
		CustomFields:         parseCustomFieldPatch(r),
	}
	if !r.Form.Has("customer_id") {
		params.CustomerID = nil
	}
	if !r.Form.Has("location_id") {
		params.LocationID = nil
	}
	if !r.Form.Has("asset_type_id") {
		params.AssetTypeID = nil
	}
	if r.Form.Has("number") {
		params.Number = strPtr(r.FormValue("number"))
	}
	if r.Form.Has("ownership_class") {
		params.OwnershipClass = strPtr(r.FormValue("ownership_class"))
	}
	if !assetDates(w, r, &params.InstalledAt, &params.WarrantyExpires) {
		return
	}

	result, err := h.svc.SaveDetails(r.Context(), u.CompanyID, id, params)
	if err != nil {
		if renderFieldSaveError(w, r, h.cfSvc, "asset", err) {
			return
		}
		writeAssetVocabularyError(w, r, err)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeAsset, id), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/assets/%d?flash=Asset+updated", id), http.StatusSeeOther)
}

func (h *AssetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	asset, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := asset.Name
	if err := h.svc.Archive(r.Context(), id); err != nil {
		writeAssetVocabularyError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeAsset, id), map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/assets?flash=Asset+archived", http.StatusSeeOther)
}

func (h *AssetHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	a, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.svc.Restore(r.Context(), id); err != nil {
		writeAssetVocabularyError(w, r, err)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeAsset, id), map[string]interface{}{
			"entity_name": a.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/assets/"+strconv.FormatInt(id, 10)+"?flash=Asset+restored", http.StatusSeeOther)
}

func (h *AssetHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if _, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeAsset, id)); err != nil {
		writeTagError(w, err)
		return
	}
	if tag != nil {
		recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeAsset, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/assets/%d?flash=Tag+attached", id), http.StatusSeeOther)
}

func (h *AssetHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeAsset, id)); err != nil {
		writeTagError(w, err)
		return
	}
	if tag != nil {
		recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeAsset, id), map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/assets/%d?flash=Tag+detached", id), http.StatusSeeOther)
}

func (h *AssetHandler) GetLocations(w http.ResponseWriter, r *http.Request) {
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	if customerID == 0 {
		customerID, _ = strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	}
	selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
	locations, _ := h.locationSvc.ListByCustomer(r.Context(), customerID)
	templates.AssetLocationOptions(locationsToOptions(locations), selected).Render(r.Context(), w)
}

func assetRow(a *ent.Asset) templates.AssetRow {
	return templates.AssetRow{
		Number:         a.Number,
		OwnershipClass: a.OwnershipClass,
		ID:             a.ID,
		Name:           a.Name,
		SerialNumber:   a.SerialNumber,
		Model:          a.Model,
		Manufacturer:   a.Manufacturer,
		CustomerID:     a.CustomerID,
		LocationID:     a.LocationID,
		AssetTypeID:    a.AssetTypeID,
		AssetStatusID:  a.AssetStatusID,
	}
}

func assetToDetail(ctx context.Context, a *ent.Asset, assetTypes []*ent.AssetType, assetStatuses []*ent.AssetStatus) *templates.AssetDetail {
	var assetTypeName, assetStatusName, assetStatusColor string
	for _, t := range assetTypes {
		if t.ID == a.AssetTypeID {
			assetTypeName = t.Name
			break
		}
	}
	for _, s := range assetStatuses {
		if a.AssetStatusID != nil && s.ID == *a.AssetStatusID {
			assetStatusName = s.Name
			assetStatusColor = s.Color
			break
		}
	}

	d := &templates.AssetDetail{
		Number:           a.Number,
		OwnershipClass:   a.OwnershipClass,
		ID:               a.ID,
		CustomerID:       a.CustomerID,
		LocationID:       a.LocationID,
		AssetTypeID:      a.AssetTypeID,
		AssetStatusID:    a.AssetStatusID,
		Name:             a.Name,
		SerialNumber:     a.SerialNumber,
		Model:            a.Model,
		Manufacturer:     a.Manufacturer,
		Notes:            a.Notes,
		InstalledAt:      a.InstalledAt,
		WarrantyExpires:  a.WarrantyExpires,
		CustomFields:     a.CustomFields,
		AssetTypeName:    assetTypeName,
		AssetStatusName:  assetStatusName,
		AssetStatusColor: assetStatusColor,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
	if a.DeletedAt != nil && !a.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(ctx, *a.DeletedAt)
	}
	return d
}

func assetDates(w http.ResponseWriter, r *http.Request, installed, warranty **time.Time) bool {
	for key, dest := range map[string]**time.Time{"installed_at": installed, "warranty_expires": warranty} {
		if value := r.FormValue(key); value != "" {
			date, err := time.ParseInLocation("2006-01-02", value, middleware.CompanyLocation(r.Context()))
			if err != nil {
				http.Error(w, "Invalid Asset date", 400)
				return false
			}
			*dest = &date
		}
	}
	return true
}

func assetSelectionInput(w http.ResponseWriter, r *http.Request) bool {
	for _, key := range []string{"customer_id", "location_id", "asset_type_id", "asset_status_id"} {
		if !r.Form.Has(key) {
			continue
		}
		n, err := strconv.ParseInt(r.FormValue(key), 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "Invalid Asset selection", 400)
			return false
		}
	}
	return true
}

func assetTextPatch(r *http.Request, key string) *string {
	if !r.Form.Has(key) {
		return nil
	}
	return strPtr(r.FormValue(key))
}

func jobRowForAsset(j *ent.Job) templates.JobRow {
	return templates.JobRow{
		ID:          j.ID,
		JobType:     j.JobType,
		DisplayName: j.JobType,
		StartTime:   "",
		StatusID:    0,
	}
}

func assetTypesToOptions(types []*ent.AssetType) []templates.SelectOption {
	options := make([]templates.SelectOption, len(types))
	for i, t := range types {
		options[i] = templates.SelectOption{Value: t.ID, Label: t.Name, Retired: t.Retired}
	}
	return options
}

func assetStatusesToOptions(statuses []*ent.AssetStatus) []templates.SelectOption {
	options := make([]templates.SelectOption, len(statuses))
	for i, s := range statuses {
		options[i] = templates.SelectOption{Value: s.ID, Label: s.Name, Retired: s.Retired}
	}
	return options
}

func customersToOptions(customers []*ent.Customer) []templates.SelectOption {
	options := make([]templates.SelectOption, len(customers))
	for i, c := range customers {
		options[i] = templates.SelectOption{Value: c.ID, Label: c.DisplayName}
	}
	return options
}

func locationsToOptions(locations []*ent.Location) []templates.SelectOption {
	options := make([]templates.SelectOption, len(locations))
	for i, l := range locations {
		options[i] = templates.SelectOption{Value: l.ID, Label: l.Title}
	}
	return options
}
