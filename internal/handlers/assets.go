package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

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

	assets, total, err := h.svc.List(r.Context(), search, customerID, assetTypeID, assetStatusID, page, perPage)
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

	// Get service history
	serviceHistory, _ := h.svc.GetServiceHistory(r.Context(), id)
	if !isAdminOrDispatcher(u) {
		serviceHistory = filterReadableJobs(r.Context(), h.policySvc, u, serviceHistory)
	}
	jobRows := make([]templates.JobRow, len(serviceHistory))
	for i, j := range serviceHistory {
		jobRows[i] = jobRowForAsset(j)
	}

	// Get tags
	tagLinks, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeAsset, id))
	var allTags []*ent.Tag
	if isAdminOrDispatcher(u) {
		allTags, _ = h.tagSvc.ListAll(r.Context(), u.CompanyID)
	}
	assignedTags := make([]templates.TagRow, 0, len(tagLinks))
	for _, tl := range tagLinks {
		assignedTags = append(assignedTags, templates.TagRow{
			ID:    tl.ID,
			Name:  tl.Name,
			Color: tl.Color,
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
				ID:    t.ID,
				Name:  t.Name,
				Color: t.Color,
			})
		}
	}

	// Custom fields
	defs, _ := h.cfSvc.ListForObjectType(r.Context(), "asset")
	cfDisplay := buildCustomFieldDisplay(defs, asset.CustomFields)

	assetDetail := assetToDetail(r.Context(), asset, assetTypes, assetStatuses)
	files, _ := h.fileSvc.List(r.Context(), u.CompanyID, objectref.New(objectref.TypeAsset, id))
	data := templates.AssetShowPageData{
		Asset:          *assetDetail,
		ServiceHistory: jobRows,
		Tags:           assignedTags,
		AllTags:        availableTags,
		CustomFields:   cfDisplay,
		FileList:       templates.FileListPageData{Files: filesToRows(r.Context(), files), ObjectID: id, ObjectType: "asset"},
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
		templates.AssetForm(templates.AssetFormPageData{
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
		CustomerID:    customerID,
		LocationID:    locID,
		AssetTypeID:   assetTypeID,
		AssetStatusID: statusID,
		Name:          r.FormValue("name"),
		SerialNumber:  r.FormValue("serial_number"),
		Model:         r.FormValue("model"),
		Manufacturer:  r.FormValue("manufacturer"),
		Notes:         r.FormValue("notes"),
		CustomFields:  parseCustomFieldValues(r),
	}

	result, err := h.svc.Create(r.Context(), u.CompanyID, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if customerID <= 0 {
		http.Error(w, "customer is required", http.StatusBadRequest)
		return
	}
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
	loc := middleware.CompanyLocation(r.Context())
	result, err := h.svc.Create(r.Context(), u.CompanyID, services.AssetCreateParams{
		CustomerID:      customerID,
		LocationID:      locID,
		AssetTypeID:     assetTypeID,
		AssetStatusID:   statusID,
		Name:            name,
		SerialNumber:    r.FormValue("serial_number"),
		Model:           r.FormValue("model"),
		Manufacturer:    r.FormValue("manufacturer"),
		Notes:           r.FormValue("notes"),
		InstalledAt:     parseDatePtr(r.FormValue("installed_at"), loc),
		WarrantyExpires: parseDatePtr(r.FormValue("warranty_expires"), loc),
		CustomFields:    "[]",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		templates.AssetForm(templates.AssetFormPageData{
			IsNew:         false,
			Asset:         assetToDetail(r.Context(), asset, assetTypes, assetStatuses),
			Customers:     customersToOptions(customers),
			Locations:     locationsToOptions(locations),
			AssetTypes:    assetTypesToOptions(assetTypes),
			AssetStatuses: assetStatusesToOptions(assetStatuses),
		}).Render(r.Context(), w)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	assetTypeID, _ := strconv.ParseInt(r.FormValue("asset_type_id"), 10, 64)
	assetStatusID, _ := strconv.ParseInt(r.FormValue("asset_status_id"), 10, 64)

	var statusID *int64
	if assetStatusID > 0 {
		statusID = &assetStatusID
	}

	params := services.AssetUpdateParams{
		CustomerID:    &customerID,
		LocationID:    &locationID,
		AssetTypeID:   &assetTypeID,
		AssetStatusID: statusID,
		Name:          strPtr(r.FormValue("name")),
		SerialNumber:  strPtr(r.FormValue("serial_number")),
		Model:         strPtr(r.FormValue("model")),
		Manufacturer:  strPtr(r.FormValue("manufacturer")),
		Notes:         strPtr(r.FormValue("notes")),
		CustomFields:  strPtr(parseCustomFieldValues(r)),
	}

	result, err := h.svc.Update(r.Context(), u.CompanyID, id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
		http.Error(w, err.Error(), 500)
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
		http.Error(w, err.Error(), 500)
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
		ID:            a.ID,
		Name:          a.Name,
		SerialNumber:  a.SerialNumber,
		Model:         a.Model,
		Manufacturer:  a.Manufacturer,
		CustomerID:    a.CustomerID,
		LocationID:    a.LocationID,
		AssetTypeID:   a.AssetTypeID,
		AssetStatusID: a.AssetStatusID,
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
		options[i] = templates.SelectOption{Value: t.ID, Label: t.Name}
	}
	return options
}

func assetStatusesToOptions(statuses []*ent.AssetStatus) []templates.SelectOption {
	options := make([]templates.SelectOption, len(statuses))
	for i, s := range statuses {
		options[i] = templates.SelectOption{Value: s.ID, Label: s.Name}
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
