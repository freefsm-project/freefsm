package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type ProjectHandler struct {
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
		templates.ProjectTable(data).Render(r.Context(), w)
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
	if !ok || u == nil || !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "project", id, policyRead) {
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
	jobs, _ := h.jobSvc.ListByProject(r.Context(), id)
	if !isAdminOrDispatcher(u) {
		jobs = filterReadableJobs(r.Context(), h.policySvc, u, jobs)
	}

	jobRows := make([]templates.JobRow, len(jobs))
	for i, j := range jobs {
		jobRows[i] = jobRow(r.Context(), j, statuses, custMap)
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
		CompletionPercentage: p.CompletionPercentage,
		Notes:                p.Notes,
	}
	if p.LocationID != nil {
		d.LocationID = *p.LocationID
		if l, err := h.locSvc.GetByCustomer(r.Context(), p.CustomerID, *p.LocationID); err == nil {
			d.LocationName = l.Title
		}
	}
	if p.StartTime != nil && !p.StartTime.IsZero() {
		d.StartTime = p.StartTime.Format("2006-01-02")
		d.StartTimeDisplay = displayDate(r.Context(), *p.StartTime)
	}
	if p.EndTime != nil && !p.EndTime.IsZero() {
		d.EndTime = p.EndTime.Format("2006-01-02")
		d.EndTimeDisplay = displayDate(r.Context(), *p.EndTime)
	}
	if p.DeletedAt != nil && !p.DeletedAt.IsZero() {
		d.ArchivedAt = displayDate(r.Context(), *p.DeletedAt)
	}

	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeProject, id))
	var allTags []*ent.Tag
	if isAdminOrDispatcher(u) {
		allTags, _ = h.tagSvc.ListAll(r.Context())
	}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "project")
	ctx := middleware.WithPageHeaderTitle(r.Context(), p.Name)
	templates.ProjectShow(templates.ProjectShowPageData{
		Project:      d,
		Jobs:         jobRows,
		Tags:         tagsToRows(tags),
		AllTags:      tagsToRows(allTags),
		CustomFields: buildCustomFieldDisplay(defs, p.CustomFields),
	}).Render(ctx, w)
}

func filterReadableJobs(ctx context.Context, policySvc *services.PolicyService, u *middleware.UserInfo, jobs []*ent.Job) []*ent.Job {
	filtered := make([]*ent.Job, 0, len(jobs))
	for _, j := range jobs {
		if policySvc.CanAccessObject(ctx, u.ID, u.Role, "job", j.ID, policyRead) {
			filtered = append(filtered, j)
		}
	}
	return filtered
}

func (h *ProjectHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, objectref.New(objectref.TypeProject, id))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	tagName := ""
	if tag != nil {
		tagName = tag.Name
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", "project", id, map[string]interface{}{
			"tag_name":   tagName,
			"actor_name": u.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeProject, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/projects/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *ProjectHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	tagName := ""
	if tag != nil {
		tagName = tag.Name
	}
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, objectref.New(objectref.TypeProject, id)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", "project", id, map[string]interface{}{
			"tag_name":   tagName,
			"actor_name": u.Name,
		})
	}
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), objectref.New(objectref.TypeProject, id))
	allTags, _ := h.tagSvc.ListAll(r.Context())
	templates.TagWidget(templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/projects/%d", id),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}).Render(r.Context(), w)
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.ProjectForm(h.newProjectForm(r.Context())).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	completion, _ := strconv.ParseFloat(r.FormValue("completion_percentage"), 64)
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
			CustomerID:           custID,
			StatusID:             statusID,
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

	loc := middleware.CompanyLocation(r.Context())
	params := services.ProjectCreateParams{
		CustomerID:           custID,
		Name:                 name,
		Description:          r.FormValue("description"),
		StatusID:             statusID,
		LocationID:           locationID,
		CompletionPercentage: completion,
		StartTime:            parseDatePtr(r.FormValue("start_time"), loc),
		EndTime:              parseDatePtr(r.FormValue("end_time"), loc),
		Notes:                r.FormValue("notes"),
		CustomFields:         parseCustomFieldValues(r),
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "project", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/projects?flash=Project+created", http.StatusSeeOther)
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
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
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	completion, _ := strconv.ParseFloat(r.FormValue("completion_percentage"), 64)
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
			CustomerID:           custID,
			StatusID:             statusID,
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
			IsNew:        false,
			Customers:    customerOptions(customers),
			Statuses:     statusOptions(statuses),
			Locations:    locationOptions(locations),
			CustomFields: buildCustomFieldDisplay(defs, parseCustomFieldValues(r)),
		}).Render(r.Context(), w)
		return
	}

	loc := middleware.CompanyLocation(r.Context())
	params := services.ProjectUpdateParams{
		CustomerID:           int64Ptr(custID),
		Name:                 formPtr(r.FormValue("name")),
		Description:          formPtr(r.FormValue("description")),
		StatusID:             int64Ptr(statusID),
		LocationID:           &locationID,
		CompletionPercentage: &completion,
		StartTime:            parseDatePtr(r.FormValue("start_time"), loc),
		EndTime:              parseDatePtr(r.FormValue("end_time"), loc),
		Notes:                formPtr(r.FormValue("notes")),
		CustomFields:         strPtr(parseCustomFieldValues(r)),
	}
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "project", id, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d?flash=Project+updated", id), http.StatusSeeOther)
}

func (h *ProjectHandler) CreateInline(w http.ResponseWriter, r *http.Request) {
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
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	if ok, err := h.statusSvc.BelongsToObjectType(r.Context(), statusID, "project"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.Error(w, "invalid project status", http.StatusBadRequest)
		return
	}
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	completion, _ := strconv.ParseFloat(r.FormValue("completion_percentage"), 64)
	loc := middleware.CompanyLocation(r.Context())
	result, err := h.svc.Create(r.Context(), services.ProjectCreateParams{
		CustomerID:           custID,
		Name:                 name,
		Description:          r.FormValue("description"),
		StatusID:             statusID,
		LocationID:           locationID,
		CompletionPercentage: completion,
		StartTime:            parseDatePtr(r.FormValue("start_time"), loc),
		EndTime:              parseDatePtr(r.FormValue("end_time"), loc),
		Notes:                r.FormValue("notes"),
		CustomFields:         "[]",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "project", result.ID, map[string]interface{}{
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
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "project", id, map[string]interface{}{
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
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "restored", "project", id, map[string]interface{}{
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
		CustomFields: buildCustomFieldDisplay(defs, "[]"),
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
	}
	if p.LocationID != nil {
		d.LocationID = *p.LocationID
	}
	if p.StartTime != nil && !p.StartTime.IsZero() {
		d.StartTime = p.StartTime.Format("2006-01-02")
	}
	if p.EndTime != nil && !p.EndTime.IsZero() {
		d.EndTime = p.EndTime.Format("2006-01-02")
	}
	return templates.ProjectFormPageData{
		Project:      &d,
		IsNew:        false,
		Customers:    customerOptions(customers),
		Statuses:     statusOptions(statuses),
		Locations:    locationOptions(locations),
		CustomFields: buildCustomFieldDisplay(defs, p.CustomFields),
	}
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
		CompletionPercentage: p.CompletionPercentage,
		StartTime:            formatProjectDisplayDate(ctx, p.StartTime),
		EndTime:              formatProjectDisplayDate(ctx, p.EndTime),
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
	return displayDate(ctx, *t)
}
