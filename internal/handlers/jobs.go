package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type JobHandler struct {
	svc         *services.JobService
	custSvc     *services.CustomerService
	statusSvc   *services.StatusService
	projectSvc  *services.ProjectService
	locSvc      *services.LocationService
	contactSvc  *services.CustomerContactService
	tagSvc      *services.TagService
	tagLinkSvc  *services.TagLinkService
	defSvc      *services.CustomFieldDefinitionService
	assetSvc    *services.AssetService
	fileSvc     *services.FileService
	activitySvc *services.ActivityService
	userSvc     *services.UserService
	policySvc   *services.PolicyService
}

func NewJobHandler(svc *services.JobService, custSvc *services.CustomerService, statusSvc *services.StatusService, projectSvc *services.ProjectService, locSvc *services.LocationService, contactSvc *services.CustomerContactService, tagSvc *services.TagService, tagLinkSvc *services.TagLinkService, defSvc *services.CustomFieldDefinitionService, assetSvc *services.AssetService, fileSvc *services.FileService, activitySvc *services.ActivityService, userSvc *services.UserService, policySvc *services.PolicyService) *JobHandler {
	return &JobHandler{svc: svc, custSvc: custSvc, statusSvc: statusSvc, projectSvc: projectSvc, locSvc: locSvc, contactSvc: contactSvc, tagSvc: tagSvc, tagLinkSvc: tagLinkSvc, defSvc: defSvc, assetSvc: assetSvc, fileSvc: fileSvc, activitySvc: activitySvc, userSvc: userSvc, policySvc: policySvc}
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)

	u, _ := middleware.UserFromContext(r.Context())
	var jobs []*ent.Job
	var total int
	var err error
	if isAdminOrDispatcher(u) {
		jobs, total, err = h.svc.List(r.Context(), search, statusID, page, perPage)
	} else if u != nil {
		jobs, total, err = h.svc.ListAssigned(r.Context(), u.ID, search, statusID, page, perPage)
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	statuses := h.statusesForSelect(r.Context())
	customers, _ := h.custSvc.ListAll(r.Context())
	custMap := customerMap(customers)

	rows := make([]templates.JobRow, len(jobs))
	for i, j := range jobs {
		rows[i] = jobRow(j, statuses, custMap)
	}

	data := templates.JobListPageData{
		Jobs:       rows,
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: services.JobPaginationTotalPages(total, perPage),
		Search:     search,
		StatusID:   statusID,
		Statuses:   statusOptions(statuses),
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		templates.JobsTable(data).Render(r.Context(), w)
		return
	}
	templates.JobsIndex(data).Render(r.Context(), w)
}

func (h *JobHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, "job", id, policyRead) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	d := jobToDetail(j, statuses)
	if j.CustomerID > 0 {
		customer, _ := h.custSvc.GetByID(r.Context(), j.CustomerID)
		if customer != nil {
			d.Customer = customer.DisplayName
		}
	}
	d.LineItems = h.svc.LineItems(j)
	d.Visits = services.ParseVisits(j.Visits)
	assignments, _ := h.svc.Assignments(r.Context(), j.ID)
	if len(assignments) == 0 {
		assignments = services.ParseAssignments(j.Assignments)
	}
	d.Assignments = assignments
	d.Subtasks = services.ParseSubtasks(j.Subtasks)
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "job", j.ID)
	var allTags []*ent.Tag
	if isAdminOrDispatcher(u) {
		allTags, _ = h.tagSvc.ListAll(r.Context())
	}
	d.Tags = tagsToRows(tags)
	d.AllTags = tagsToRows(allTags)
	if j.ProjectID != nil && *j.ProjectID > 0 {
		project, _ := h.projectSvc.GetByID(r.Context(), *j.ProjectID)
		if project != nil && project.DeletedAt == nil {
			d.ProjectName = project.Name
		}
	}
	if j.LocationID != nil && *j.LocationID > 0 {
		location, _ := h.locSvc.GetByID(r.Context(), *j.LocationID)
		if location != nil {
			d.LocationName = location.Title
		}
	}
	if j.CustomerContactID != nil && *j.CustomerContactID > 0 {
		contact, _ := h.contactSvc.GetByID(r.Context(), *j.CustomerContactID)
		if contact != nil && contact.CustomerID == j.CustomerID {
			d.ContactName = contact.FirstName + " " + contact.LastName
		}
	}
	if j.AssetID != nil && *j.AssetID > 0 {
		asset, _ := h.assetSvc.GetByID(r.Context(), *j.AssetID)
		if asset != nil {
			d.AssetName = asset.Name
		}
	}
	defs, _ := h.defSvc.ListForObjectType(r.Context(), "job")
	d.CustomFields = buildCustomFieldDisplay(defs, j.CustomFields)
	files, _ := h.fileSvc.List(r.Context(), "job", j.ID)
	d.FileList = templates.FileListPageData{Files: filesToRows(files), ObjectID: j.ID, ObjectType: "job"}
	ctx := middleware.WithPageHeaderTitle(r.Context(), j.JobType)
	templates.JobShow(d).Render(ctx, w)
}

func (h *JobHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.JobForm(h.newJobForm(r.Context())).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("customer_contact_id"), 10, 64)
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	loc := middleware.CompanyLocation(r.Context())
	params := services.JobCreateParams{
		CustomerID:        custID,
		ProjectID:         projectID,
		LocationID:        locationID,
		CustomerContactID: contactID,
		AssetID:           assetID,
		JobType:           r.FormValue("job_type"),
		Subtitle:          r.FormValue("subtitle"),
		StatusID:          statusID,
		BillingType:       r.FormValue("billing_type"),
		StartTime:         parseTime(r.FormValue("start_time"), loc),
		EndTime:           parseTime(r.FormValue("end_time"), loc),
		DueDate:           parseDate(r.FormValue("due_date"), loc),
		Notes:             r.FormValue("notes"),
		TechNotes:         r.FormValue("tech_notes"),
		Visits:            services.ParseVisits(r.FormValue("visits")),
		Assignments:       services.ParseAssignments(r.FormValue("assignments")),
		Subtasks:          services.ParseSubtasks(r.FormValue("subtasks")),
		CustomFields:      parseCustomFieldValues(r),
	}
	if params.BillingType == "" {
		params.BillingType = "flat_rate"
	}
	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "created", "job", result.ID, map[string]interface{}{
			"entity_name": result.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs?flash=Job+created", http.StatusSeeOther)
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		j, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statuses := h.statusesForSelect(r.Context())
		fd := h.formDataFromJob(r.Context(), j, statuses)
		templates.JobForm(fd).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	custID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	statusID, _ := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	locationID, _ := strconv.ParseInt(r.FormValue("location_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("customer_contact_id"), 10, 64)
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	params := services.JobUpdateParams{
		CustomerID:        int64Ptr(custID),
		ProjectID:         int64Ptr(projectID),
		LocationID:        int64Ptr(locationID),
		CustomerContactID: int64Ptr(contactID),
		AssetID:           int64Ptr(assetID),
		JobType:           formPtr(r.FormValue("job_type")),
		Subtitle:          formPtr(r.FormValue("subtitle")),
		StatusID:          int64Ptr(statusID),
		BillingType:       formPtr(r.FormValue("billing_type")),
		Notes:             formPtr(r.FormValue("notes")),
		TechNotes:         formPtr(r.FormValue("tech_notes")),
		CustomFields:      strPtr(parseCustomFieldValues(r)),
	}
	if v := r.FormValue("visits"); v != "" {
		visits := services.ParseVisits(v)
		params.Visits = &visits
	}
	if a := r.FormValue("assignments"); a != "" {
		assignments := services.ParseAssignments(a)
		params.Assignments = &assignments
	}
	if s := r.FormValue("subtasks"); s != "" {
		subtasks := services.ParseSubtasks(s)
		params.Subtasks = &subtasks
	}
	loc := middleware.CompanyLocation(r.Context())
	if st := r.FormValue("start_time"); st != "" {
		t := parseTime(st, loc)
		params.StartTime = &t
	}
	if et := r.FormValue("end_time"); et != "" {
		t := parseTime(et, loc)
		params.EndTime = &t
	}
	if dd := r.FormValue("due_date"); dd != "" {
		t := parseDate(dd, loc)
		params.DueDate = &t
	}
	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "updated", "job", id, map[string]interface{}{
			"entity_name": result.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=Job+updated", id), http.StatusSeeOther)
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entityName := j.JobType
	if err := h.svc.Archive(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "archived", "job", id, map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs?flash=Job+archived", http.StatusSeeOther)
}

func (h *JobHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
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
		h.activitySvc.Record(r.Context(), u.ID, "restored", "job", id, map[string]interface{}{
			"entity_name": j.JobType,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10)+"?flash=Job+restored", http.StatusSeeOther)
}

func (h *JobHandler) ToggleSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subtasks := services.ParseSubtasks(j.Subtasks)
	if idx < 0 || idx >= len(subtasks) {
		http.NotFound(w, r)
		return
	}
	subtasks[idx].Completed = !subtasks[idx].Completed
	params := services.JobUpdateParams{
		Subtasks: &subtasks,
	}
	if _, err := h.svc.Update(r.Context(), id, params); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		action := "subtask_completed"
		if !subtasks[idx].Completed {
			action = "subtask_uncompleted"
		}
		h.activitySvc.Record(r.Context(), u.ID, action, "job", id, map[string]interface{}{
			"actor_name":  u.Name,
			"entity_name": subtasks[idx].Title,
		})
	}
	statuses := h.statusesForSelect(r.Context())
	d := jobToDetail(j, statuses)
	d.Subtasks = subtasks
	templates.JobSubtasks(d).Render(r.Context(), w)
}

func (h *JobHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "job")
	return statuses
}

func (h *JobHandler) newJobForm(ctx context.Context) templates.JobFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	projects, _ := h.projectSvc.ListAll(ctx)
	locations, _ := h.locSvc.ListAll(ctx)
	assets, _ := h.assetSvc.ListAll(ctx)
	users, _ := h.userSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "job")
	return templates.JobFormPageData{
		Job: &templates.JobDetail{
			BillingType: "flat_rate",
			StartTime:   time.Now().Format("2006-01-02") + "T08:00",
		},
		IsNew:                   true,
		Customers:               customerOptions(customers),
		Projects:                projectOptions(projects),
		Locations:               locationOptions(locations),
		Assets:                  assetOptions(assets),
		Users:                   userOptions(users),
		Statuses:                statusOptions(statuses),
		BillingTypes:            services.JobBillingTypes,
		ExistingVisitsJSON:      "[]",
		ExistingAssignmentsJSON: "[]",
		ExistingSubtasksJSON:    "[]",
		CustomFields:            buildCustomFieldDisplay(defs, "[]"),
	}
}

func (h *JobHandler) formDataFromJob(ctx context.Context, j *ent.Job, statuses []*ent.Status) templates.JobFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	projects, _ := h.projectSvc.ListAll(ctx)
	locations, _ := h.locSvc.ListAll(ctx)
	assets, _ := h.assetSvc.ListAll(ctx)
	users, _ := h.userSvc.ListAll(ctx)
	defs, _ := h.defSvc.ListForObjectType(ctx, "job")
	d := jobToDetail(j, statuses)
	assignments, _ := h.svc.Assignments(ctx, j.ID)
	if len(assignments) == 0 {
		assignments = services.ParseAssignments(j.Assignments)
	}
	return templates.JobFormPageData{
		Job:                     &d,
		IsNew:                   false,
		Customers:               customerOptions(customers),
		Projects:                projectOptions(projects),
		Locations:               locationOptions(locations),
		Assets:                  assetOptions(assets),
		Users:                   userOptions(users),
		Statuses:                statusOptions(statuses),
		BillingTypes:            services.JobBillingTypes,
		ExistingVisitsJSON:      services.SerializeVisits(services.ParseVisits(j.Visits)),
		ExistingAssignmentsJSON: services.SerializeAssignments(assignments),
		ExistingSubtasksJSON:    services.SerializeSubtasks(services.ParseSubtasks(j.Subtasks)),
		CustomFields:            buildCustomFieldDisplay(defs, j.CustomFields),
	}
}

func userOptions(users []*ent.User) []templates.SelectOption {
	opts := make([]templates.SelectOption, 0, len(users))
	for _, u := range users {
		if !u.IsActive {
			continue
		}
		label := u.Name
		if u.Role != "" {
			label = fmt.Sprintf("%s (%s)", u.Name, u.Role)
		}
		opts = append(opts, templates.SelectOption{Value: u.ID, Label: label})
	}
	return opts
}

func projectOptions(projects []*ent.Project) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(projects))
	for i, p := range projects {
		opts[i] = templates.SelectOption{Value: p.ID, Label: p.Name}
	}
	return opts
}

func locationOptions(locations []*ent.Location) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(locations))
	for i, l := range locations {
		opts[i] = templates.SelectOption{Value: l.ID, Label: l.Title}
	}
	return opts
}

func assetOptions(assets []*ent.Asset) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(assets))
	for i, a := range assets {
		opts[i] = templates.SelectOption{Value: a.ID, Label: a.Name}
	}
	return opts
}

func jobToDetail(j *ent.Job, statuses []*ent.Status) templates.JobDetail {
	d := templates.JobDetail{
		ID:          j.ID,
		CustomerID:  j.CustomerID,
		JobType:     j.JobType,
		Subtitle:    j.Subtitle,
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		StatusColor: statusColor(statuses, j.StatusID),
		BillingType: j.BillingType,
		Notes:       j.Notes,
		TechNotes:   j.TechNotes,
	}
	if j.ProjectID != nil {
		d.ProjectID = *j.ProjectID
	}
	if j.LocationID != nil {
		d.LocationID = *j.LocationID
	}
	if j.CustomerContactID != nil {
		d.ContactID = *j.CustomerContactID
	}
	if j.AssetID != nil {
		d.AssetID = *j.AssetID
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		d.StartTime = j.StartTime.Format("2006-01-02T15:04")
	}
	if j.EndTime != nil && !j.EndTime.IsZero() {
		d.EndTime = j.EndTime.Format("2006-01-02T15:04")
	}
	if j.DueDate != nil && !j.DueDate.IsZero() {
		d.DueDate = j.DueDate.Format("2006-01-02")
	}
	if j.DeletedAt != nil && !j.DeletedAt.IsZero() {
		d.ArchivedAt = j.DeletedAt.Format("Jan 2, 2006")
	}
	return d
}

func jobRow(j *ent.Job, statuses []*ent.Status, custMap map[int64]string) templates.JobRow {
	r := templates.JobRow{
		ID:          j.ID,
		DisplayName: j.JobType,
		JobType:     j.JobType,
		Customer:    custMap[j.CustomerID],
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		StatusColor: statusColor(statuses, j.StatusID),
		BillingType: j.BillingType,
	}
	if j.Subtitle != "" {
		r.DisplayName = j.JobType + " — " + j.Subtitle
	}
	if j.StartTime != nil && !j.StartTime.IsZero() {
		r.StartTime = j.StartTime.Format("Jan 2, 2006 3:04 PM")
	}
	return r
}

func parseTime(v string, loc *time.Location) time.Time {
	t, _ := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(v), loc)
	return t
}

func parseDate(v string, loc *time.Location) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", strings.TrimSpace(v), loc)
	return t
}

func statusID(j *ent.Job) int64 {
	if j.StatusID == nil {
		return 0
	}
	return *j.StatusID
}

func (h *JobHandler) AttachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	_, err := h.tagLinkSvc.Attach(r.Context(), tagID, "job", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil && tag != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_attached", "job", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	h.loadTagWidget(w, r, id)
}

func (h *JobHandler) DetachTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	tag, _ := h.tagSvc.GetByID(r.Context(), tagID)
	if err := h.tagLinkSvc.Detach(r.Context(), tagID, "job", id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil && tag != nil {
		h.activitySvc.Record(r.Context(), u.ID, "tag_detached", "job", id, map[string]interface{}{
			"actor_name": u.Name,
			"tag_name":   tag.Name,
		})
	}
	h.loadTagWidget(w, r, id)
}

func (h *JobHandler) loadTagWidget(w http.ResponseWriter, r *http.Request, jobID int64) {
	tags, _ := h.tagLinkSvc.ListForObject(r.Context(), "job", jobID)
	allTags, _ := h.tagSvc.ListAll(r.Context())
	d := templates.TagWidgetData{
		BaseURL: fmt.Sprintf("/jobs/%d", jobID),
		Tags:    tagsToRows(tags),
		AllTags: tagsToRows(allTags),
	}
	templates.TagWidget(d).Render(r.Context(), w)
}

func tagsToRows(tags []*ent.Tag) []templates.TagRow {
	rows := make([]templates.TagRow, len(tags))
	for i, t := range tags {
		rows[i] = templates.TagRow{
			ID:    t.ID,
			Name:  t.Name,
			Color: t.Color,
		}
	}
	return rows
}
