package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type ProjectHandler struct {
	fileSvc     *services.FileService
	svc         *services.ProjectService
	custSvc     *services.CustomerService
	statusSvc   *services.StatusService
	locSvc      *services.LocationService
	jobSvc      *services.JobService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	statusflow  *statusflow.Service
}

func NewProjectHandler(svc *services.ProjectService, custSvc *services.CustomerService, statusSvc *services.StatusService, locSvc *services.LocationService, jobSvc *services.JobService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, activitySvc *services.ActivityService, policySvc *services.PolicyService) *ProjectHandler {
	return &ProjectHandler{svc: svc, custSvc: custSvc, statusSvc: statusSvc, locSvc: locSvc, jobSvc: jobSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, activitySvc: activitySvc, policySvc: policySvc}
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)

	projects, total, err := h.svc.List(r.Context(), search, statusID, page, perPage)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.ProjectRow, len(projects))
	for i, p := range projects {
		rows[i] = projectRow(r.Context(), p, statuses, custMap)
	}

	data := templates.ProjectListPageData{
		Projects:   rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.ProjectPaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.ProjectListContent(data).Render(r.Context(), w)
		return
	}
	templates.ProjectIndex(data).Render(r.Context(), w)
}

func (h *ProjectHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, objectref.New(objectref.TypeProject, id), policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	p, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)
	jobsPage, _ := strconv.Atoi(r.URL.Query().Get("jobs_page"))
	if jobsPage < 1 {
		jobsPage = 1
	}
	jobsPerPage, _ := strconv.Atoi(r.URL.Query().Get("jobs_per_page"))
	if jobsPerPage != 50 && jobsPerPage != 100 {
		jobsPerPage = 25
	}
	jobs, jobsTotal, err := h.jobSvc.ProjectPage(r.Context(), id, jobsPage, jobsPerPage)
	if err != nil {
		http.Error(w, "Unable to load Project Jobs", 500)
		return
	}

	jobRows := make([]templates.JobRow, len(jobs))
	jobStatuses, err := h.statusSvc.ByObjectType(r.Context(), "job")
	if err != nil {
		http.Error(w, "Unable to load Job statuses", 500)
		return
	}
	for i, j := range jobs {
		jobRows[i] = jobRow(r.Context(), j, jobStatuses, custMap)
	}

	statusesMap := statusMap(statuses)
	d := templates.ProjectDetail{
		ID:                   p.ID,
		Name:                 p.Name,
		Description:          p.Description,
		CustomerID:           p.CustomerID,
		CustomerName:         custMap[p.CustomerID],
		StatusID:             projectStatusID(p),
		StatusName:           statusesMap[projectStatusID(p)],
		StatusColor:          statusColor(statuses, p.StatusID),
		CompletionPercentage: projectProgress(p, statuses),
		Notes:                p.Notes,
		ServiceNotes:         p.ServiceNotes,
	}
	if p.LocationID != nil {
		d.LocationID = *p.LocationID
		if l, err := h.locSvc.GetByCustomer(r.Context(), p.CustomerID, *p.LocationID); err == nil {
			d.LocationName = l.Title
		}
	}
	if p.PlannedStartDate != nil && !p.PlannedStartDate.IsZero() {
		d.StartTime = p.PlannedStartDate.Format("2006-01-02")
		d.StartTimeDisplay = d.StartTime
	}
	if p.PlannedEndDate != nil && !p.PlannedEndDate.IsZero() {
		d.EndTime = p.PlannedEndDate.Format("2006-01-02")
		d.EndTimeDisplay = d.EndTime
	}
	if p.DeletedAt != nil && !p.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(r.Context(), *p.DeletedAt)
	}
	applyProjectSemantics(r.Context(), &d, p, statuses)
	if request, ok := authorization.FromContext(r.Context()); ok {
		d.LegacyReview, err = request.Service.ProjectLegacyReview(r.Context(), request.Actor, id)
		if err != nil {
			http.Error(w, "Unable to load Project migration review", 500)
			return
		}
	}

	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeProject, id))
	var allTags []*ent.Tag
	if canAttachTag(r, "project", id) {
		allTags, _ = h.tagSvc.ListAll(r.Context(), u.CompanyID)
	}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "project")
	ctx := middleware.WithPageHeaderTitle(r.Context(), p.Name)
	var files []*ent.File
	if h.fileSvc != nil {
		files, _ = h.fileSvc.List(ctx, u.CompanyID, objectref.New(objectref.TypeProject, id))
	}
	templates.ProjectShow(templates.ProjectShowPageData{
		FileList: templates.FileListPageData{Files: filesToRows(ctx, files), ObjectID: id, ObjectType: "project"},
		Project:  d,
		Statuses: statusOptions(statuses),
		Jobs:     jobRows,
		JobsPage: jobsPage, JobsPerPage: jobsPerPage, JobsTotalPages: services.TotalPages(jobsTotal, jobsPerPage),
		Tags:         tagsToRows(tags),
		AllTags:      tagsToRows(allTags),
		CustomFields: buildCustomFieldDisplay(defs, p.CustomFields),
	}).Render(ctx, w)
}

func filterReadableJobs(ctx context.Context, policySvc *services.PolicyService, u *middleware.UserInfo, jobs []*ent.Job) []*ent.Job {
	filtered := make([]*ent.Job, 0, len(jobs))
	for _, j := range jobs {
		if policySvc.CanAccessObject(ctx, u.ID, u.Role, objectref.New(objectref.TypeJob, j.ID), policyRead) {
			filtered = append(filtered, j)
		}
	}
	return filtered
}

func (h *ProjectHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	_, err := h.tagLinkSvc.Attach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeProject, id))
	if err != nil {
		writeTagError(w, err)
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	tagName := ""
	if tag != nil {
		tagName = tag.Name
	}
	recordTagActivity(r, h.activitySvc, u, "tag_attached", objectref.New(objectref.TypeProject, id), map[string]interface{}{
		"tag_name":   tagName,
		"actor_name": u.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeProject, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/projects/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *ProjectHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	u, ok := requireTagCompany(w, r)
	if !ok {
		return
	}
	id, tagID, ok := tagRouteIDs(w, r)
	if !ok {
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), u.CompanyID, tagID)
	tagName := ""
	if tag != nil {
		tagName = tag.Name
	}
	if err := h.tagLinkSvc.Detach(r.Context(), u.CompanyID, tagID, objectref.New(objectref.TypeProject, id)); err != nil {
		writeTagError(w, err)
		return
	}
	recordTagActivity(r, h.activitySvc, u, "tag_detached", objectref.New(objectref.TypeProject, id), map[string]interface{}{
		"tag_name":   tagName,
		"actor_name": u.Name,
	})
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), u.CompanyID, objectref.New(objectref.TypeProject, id))
	allTags, _ := h.tagSvc.ListAll(r.Context(), u.CompanyID)
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/projects/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		templates.ProjectForm(h.newProjectForm(r.Context())).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	locationID, valid := projectLocationInput(w, r)
	if !valid {
		return
	}
	completion, valid := projectInitialProgress(w, r)
	if !valid {
		return
	}
	name := r.FormValue("name")

	errors := make(map[string]string)
	if custID == 0 {
		errors["customer_id"] = "Customer is required"
	}
	if name == "" {
		errors["name"] = "Project name is required"
	}

	if len(errors) > 0 {
		statuses := h.statusesForSelect(r.Context())
		customers, _ := h.custSvc.ListAll(r.Context())
		locations, _ := h.locSvc.ListByCustomer(r.Context(), custID)
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "project")
		d := &templates.ProjectDetail{
			Name:                 name,
			Description:          r.FormValue("description"),
			ServiceNotes:         r.FormValue("service_notes"),
			CustomerID:           custID,
			LocationID:           locationID,
			CompletionPercentage: completion,
			Notes:                r.FormValue("notes"),
		}
		if t := r.FormValue("start_time"); t != "" {
			d.StartTime = t
		}
		if t := r.FormValue("end_time"); t != "" {
			d.EndTime = t
		}
		templates.ProjectForm(templates.ProjectFormPageData{
			Project:      d,
			Errors:       errors,
			IsNew:        true,
			Customers:    customerOptions(customers),
			Statuses:     statusOptions(statuses),
			Locations:    locationOptions(locations),
			CustomFields: buildCustomFieldDisplay(defs, parseCustomFieldValues(r)),
		}).Render(r.Context(), w)
		return
	}

	start, end, valid := projectDates(w, r)
	if !valid {
		return
	}
	params := services.ProjectCreateParams{
		CustomerID:           custID,
		Name:                 name,
		Description:          r.FormValue("description"),
		StatusID:             0,
		LocationID:           locationID,
		CompletionPercentage: completion,
		StartTime:            start,
		EndTime:              end,
		Notes:                r.FormValue("notes"),
		ServiceNotes:         r.FormValue("service_notes"),
		CustomFields:         parseCustomFieldValues(r),
	}
	result, err := h.svc.Create(r.Context(), u.CompanyID, params)
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "project", err) {
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeProject, result.ID), map[string]interface{}{
		"entity_name": result.Name,
		"actor_name":  u.Name,
	})
	http.Redirect(w, r, "/projects?flash=Project+created", http.StatusSeeOther)
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		p, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statuses := h.statusesForSelect(r.Context())
		templates.ProjectForm(h.formDataFromProject(r.Context(), p, statuses)).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	keepLocation := r.FormValue("location_id") == "keep"
	var locationID int64
	if !keepLocation {
		var valid bool
		locationID, valid = projectLocationInput(w, r)
		if !valid {
			return
		}
	}
	var completion *float64
	if raw := r.FormValue("completion_percentage"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 || v > 100 {
			http.Error(w, "Progress must be an integer from 0 through 100", 400)
			return
		}
		value := float64(v)
		completion = &value
	}
	name := r.FormValue("name")

	errors := make(map[string]string)
	if custID == 0 {
		errors["customer_id"] = "Customer is required"
	}
	if name == "" {
		errors["name"] = "Project name is required"
	}

	if len(errors) > 0 {
		statuses := h.statusesForSelect(r.Context())
		customers, _ := h.custSvc.ListAll(r.Context())
		locations, _ := h.locSvc.ListByCustomer(r.Context(), custID)
		defs, _ := h.defSvc.ListForObjectType(r.Context(), "project")
		d := &templates.ProjectDetail{
			ID:                   id,
			Name:                 name,
			Description:          r.FormValue("description"),
			ServiceNotes:         r.FormValue("service_notes"),
			CustomerID:           custID,
			LocationID:           locationID,
			CompletionPercentage: 0,
			Notes:                r.FormValue("notes"),
		}
		if completion != nil {
			d.CompletionPercentage = *completion
		}
		if current, err := h.svc.GetByID(r.Context(), id); err == nil {
			applyProjectSemantics(r.Context(), d, current, statuses)
			if (keepLocation || !r.PostForm.Has("location_id")) && current.LocationID != nil {
				d.LocationID = *current.LocationID
			}
		}
		if t := r.FormValue("start_time"); t != "" {
			d.StartTime = t
		}
		if t := r.FormValue("end_time"); t != "" {
			d.EndTime = t
		}
		templates.ProjectForm(templates.ProjectFormPageData{
			Project:      d,
			Errors:       errors,
			IsNew:        false,
			KeepLocation: projectLocationUnavailable(d.LocationID, locationOptions(locations)),
			Customers:    customerOptions(customers),
			Statuses:     statusOptions(statuses),
			Locations:    locationOptions(locations),
			CustomFields: buildCustomFieldDisplay(defs, parseCustomFieldValues(r)),
		}).Render(r.Context(), w)
		return
	}

	start, end, valid := projectDates(w, r)
	if !valid {
		return
	}
	params := services.ProjectUpdateParams{
		CustomerID:           int64Ptr(custID),
		Name:                 formPtr(r.FormValue("name")),
		Description:          formPtr(r.FormValue("description")),
		CompletionPercentage: completion,
		StartTime:            start,
		EndTime:              end,
		Notes:                formPtr(r.FormValue("notes")),
		ServiceNotes:         formPtr(r.FormValue("service_notes")),
		CustomFields:         parseCustomFieldPatch(r),
	}
	if r.PostForm.Has("location_id") && !keepLocation {
		params.LocationID = &locationID
	}
	result, err := h.svc.SaveDetails(r.Context(), u.CompanyID, id, params)
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "project", err) {
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "updated", objectref.New(objectref.TypeProject, id), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d?flash=Project+updated", id), http.StatusSeeOther)
}

func (h *ProjectHandler) CreateInline(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if custID <= 0 {
		http.Error(w, "customer is required", http.StatusBadRequest)
		return
	}
	customer, err := h.custSvc.GetByID(r.Context(), custID)
	if err != nil || customer.DeletedAt != nil {
		http.Error(w, "invalid customer", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "project name is required", http.StatusBadRequest)
		return
	}
	locationID, valid := projectLocationInput(w, r)
	if !valid {
		return
	}
	completion, valid := projectInitialProgress(w, r)
	if !valid {
		return
	}
	start, end, valid := projectDates(w, r)
	if !valid {
		return
	}
	result, err := h.svc.Create(r.Context(), u.CompanyID, services.ProjectCreateParams{
		CustomerID:           custID,
		Name:                 name,
		Description:          r.FormValue("description"),
		StatusID:             0,
		LocationID:           locationID,
		CompletionPercentage: completion,
		StartTime:            start,
		EndTime:              end,
		Notes:                r.FormValue("notes"),
		ServiceNotes:         r.FormValue("service_notes"),
		CustomFields:         parseCustomFieldValues(r),
	})
	if err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "project", err) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "created", objectref.New(objectref.TypeProject, result.ID), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	writeInlineOptionJSON(w, result.ID, result.Name)
}

func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	p, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := p.Name
	if err := h.svc.Archive(r.Context(), id); err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "project", err) {
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "archived", objectref.New(objectref.TypeProject, id), map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/projects?flash=Project+archived", http.StatusSeeOther)
}

func (h *ProjectHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	p, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.svc.Restore(r.Context(), id); err != nil {
		if renderFieldSaveError(w, r, h.defSvc, "project", err) {
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "restored", objectref.New(objectref.TypeProject, id), map[string]interface{}{
			"entity_name": p.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/projects/"+strconv.FormatInt(id, 10)+"?flash=Project+restored", http.StatusSeeOther)
}

func (h *ProjectHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "project")
	return statuses
}

func (h *ProjectHandler) ProjectOptions(w http.ResponseWriter, r *http.Request) {
	customerID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	projects, _ := h.svc.ListByCustomer(r.Context(), customerID)
	selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
	templates.ProjectOptions(projectOptions(projects), selected).Render(r.Context(), w)
}

func (h *ProjectHandler) newProjectForm(ctx context.Context) templates.ProjectFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "project")
	return templates.ProjectFormPageData{
		Project: &templates.ProjectDetail{
			CompletionPercentage: 0,
		},
		IsNew:        true,
		Customers:    customerOptions(customers),
		Statuses:     statusOptions(statuses),
		Locations:    nil,
		CustomFields: buildCustomFieldInitialDisplay(defs),
	}
}

func (h *ProjectHandler) formDataFromProject(ctx context.Context, p *ent.Project, statuses []*ent.Status) templates.ProjectFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	locations, _ := h.locSvc.ListByCustomer(ctx, p.CustomerID)
	defs, _ := h.defSvc.ListForObjectType(ctx, "project")
	statusesMap := statusMap(statuses)
	d := templates.ProjectDetail{
		ID:                   p.ID,
		Name:                 p.Name,
		Description:          p.Description,
		CustomerID:           p.CustomerID,
		StatusID:             projectStatusID(p),
		StatusName:           statusesMap[projectStatusID(p)],
		StatusColor:          statusColor(statuses, p.StatusID),
		CompletionPercentage: p.CompletionPercentage,
		Notes:                p.Notes,
		ServiceNotes:         p.ServiceNotes,
	}
	applyProjectSemantics(ctx, &d, p, statuses)
	if p.LocationID != nil {
		d.LocationID = *p.LocationID
	}
	if p.PlannedStartDate != nil && !p.PlannedStartDate.IsZero() {
		d.StartTime = p.PlannedStartDate.Format("2006-01-02")
	}
	if p.PlannedEndDate != nil && !p.PlannedEndDate.IsZero() {
		d.EndTime = p.PlannedEndDate.Format("2006-01-02")
	}
	return templates.ProjectFormPageData{
		Project:      &d,
		IsNew:        false,
		KeepLocation: projectLocationUnavailable(d.LocationID, locationOptions(locations)),
		Customers:    customerOptions(customers),
		Statuses:     statusOptions(statuses),
		Locations:    locationOptions(locations),
		CustomFields: buildCustomFieldDisplay(defs, p.CustomFields),
	}
}

func projectLocationUnavailable(id int64, options []templates.SelectOption) bool {
	if id == 0 {
		return false
	}
	for _, option := range options {
		if option.Value == id {
			return false
		}
	}
	return true
}

func projectProgress(p *ent.Project, statuses []*ent.Status) float64 {
	for _, status := range statuses {
		if p.StatusID != nil && status.ID == *p.StatusID && status.CategoryKey == "project:completed" {
			return 100
		}
	}
	return p.CompletionPercentage
}

func applyProjectSemantics(ctx context.Context, d *templates.ProjectDetail, p *ent.Project, statuses []*ent.Status) {
	for _, s := range statuses {
		if p.StatusID != nil && s.ID == *p.StatusID {
			d.Completed = s.CategoryKey == "project:completed"
			d.Archivable = d.Completed || s.CategoryKey == "project:canceled"
		}
	}
	d.Overdue = !d.Archivable && p.PlannedEndDate != nil && p.PlannedEndDate.Format("2006-01-02") < time.Now().In(middleware.CompanyLocation(ctx)).Format("2006-01-02")
}

func projectRow(ctx context.Context, p *ent.Project, statuses []*ent.Status, custMap map[int64]string) templates.ProjectRow {
	return templates.ProjectRow{
		ID:                   p.ID,
		Name:                 p.Name,
		Description:          p.Description,
		CustomerID:           p.CustomerID,
		CustomerName:         custMap[p.CustomerID],
		StatusID:             projectStatusID(p),
		StatusName:           statusName(statuses, p.StatusID),
		StatusColor:          statusColor(statuses, p.StatusID),
		CompletionPercentage: projectProgress(p, statuses),
		StartTime:            formatProjectDisplayDate(ctx, p.PlannedStartDate),
		EndTime:              formatProjectDisplayDate(ctx, p.PlannedEndDate),
	}
}

func projectStatusID(p *ent.Project) int64 {
	if p.StatusID == nil {
		return 0
	}
	return *p.StatusID
}

func parseDatePtr(v string, loc *time.Location) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	t, _ := time.ParseInLocation("2006-01-02", v, loc)
	return &t
}

func formatProjectDisplayDate(ctx context.Context, t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// A planned date is a calendar value, never an instant in the display timezone.
// A present empty field clears it; an omitted field leaves it unchanged.
func projectDates(w http.ResponseWriter, r *http.Request) (*time.Time, *time.Time, bool) {
	var dates [2]*time.Time
	for i, key := range []string{"start_time", "end_time"} {
		if !r.PostForm.Has(key) {
			continue
		}
		value := time.Time{}
		if raw := r.FormValue(key); raw != "" {
			var err error
			value, err = time.Parse("2006-01-02", raw)
			if err != nil {
				http.Error(w, "Invalid planned date", 400)
				return nil, nil, false
			}
		}
		dates[i] = &value
	}
	return dates[0], dates[1], true
}

func projectLocationInput(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if r.FormValue("location_id") == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	if err != nil || id < 0 {
		http.Error(w, "Invalid Project Location", 400)
		return 0, false
	}
	return id, true
}

func projectInitialProgress(w http.ResponseWriter, r *http.Request) (float64, bool) {
	if raw := r.FormValue("completion_percentage"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 100 {
			http.Error(w, "Progress must be an integer from 0 through 100", 400)
			return 0, false
		}
	}
	return 0, true
}
