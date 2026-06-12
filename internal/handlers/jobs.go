package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type JobHandler struct {
	svc       *services.JobService
	custSvc   *services.CustomerService
	statusSvc *services.StatusService
}

func NewJobHandler(svc *services.JobService, custSvc *services.CustomerService, statusSvc *services.StatusService) *JobHandler {
	return &JobHandler{svc: svc, custSvc: custSvc, statusSvc: statusSvc}
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 25
	search := r.URL.Query().Get("search")
	statusID, _ := strconv.ParseInt(r.URL.Query().Get("status_id"), 10, 64)

	jobs, total, err := h.svc.List(r.Context(), search, statusID, page, perPage)
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

	if r.Header.Get("HX-Request") == "true" {
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
	j, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	statuses := h.statusesForSelect(r.Context())
	templates.JobShow(jobToDetail(j, statuses)).Render(r.Context(), w)
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
	params := services.JobCreateParams{
		CustomerID:  custID,
		JobType:     r.FormValue("job_type"),
		Subtitle:    r.FormValue("subtitle"),
		StatusID:    statusID,
		BillingType: r.FormValue("billing_type"),
		StartTime:   parseTime(r.FormValue("start_time")),
		EndTime:     parseTime(r.FormValue("end_time")),
		DueDate:     parseDate(r.FormValue("due_date")),
		Notes:       r.FormValue("notes"),
		TechNotes:   r.FormValue("tech_notes"),
	}
	if params.BillingType == "" {
		params.BillingType = "flat_rate"
	}
	_, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
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
	params := services.JobUpdateParams{
		CustomerID:  int64Ptr(custID),
		JobType:     formPtr(r.FormValue("job_type")),
		Subtitle:    formPtr(r.FormValue("subtitle")),
		StatusID:    int64Ptr(statusID),
		BillingType: formPtr(r.FormValue("billing_type")),
		Notes:       formPtr(r.FormValue("notes")),
		TechNotes:   formPtr(r.FormValue("tech_notes")),
	}
	if st := r.FormValue("start_time"); st != "" {
		t := parseTime(st)
		params.StartTime = &t
	}
	if et := r.FormValue("end_time"); et != "" {
		t := parseTime(et)
		params.EndTime = &t
	}
	if dd := r.FormValue("due_date"); dd != "" {
		t := parseDate(dd)
		params.DueDate = &t
	}
	if _, err := h.svc.Update(r.Context(), id, params); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=Job+updated", id), http.StatusSeeOther)
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/jobs?flash=Job+deleted", http.StatusSeeOther)
}

func (h *JobHandler) statusesForSelect(ctx context.Context) []*ent.Status {
	statuses, _ := h.statusSvc.ByObjectType(ctx, "job")
	return statuses
}

func (h *JobHandler) newJobForm(ctx context.Context) templates.JobFormPageData {
	statuses := h.statusesForSelect(ctx)
	customers, _ := h.custSvc.ListAll(ctx)
	return templates.JobFormPageData{
		Job: &templates.JobDetail{
			BillingType: "flat_rate",
		},
		IsNew:        true,
		Customers:    customerOptions(customers),
		Statuses:     statusOptions(statuses),
		BillingTypes: services.JobBillingTypes,
	}
}

func (h *JobHandler) formDataFromJob(ctx context.Context, j *ent.Job, statuses []*ent.Status) templates.JobFormPageData {
	customers, _ := h.custSvc.ListAll(ctx)
	d := jobToDetail(j, statuses)
	return templates.JobFormPageData{
		Job:          &d,
		IsNew:        false,
		Customers:    customerOptions(customers),
		Statuses:     statusOptions(statuses),
		BillingTypes: services.JobBillingTypes,
	}
}

func statusOptions(statuses []*ent.Status) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(statuses))
	for i, s := range statuses {
		opts[i] = templates.SelectOption{Value: s.ID, Label: s.Name}
	}
	return opts
}

func customerOptions(customers []*ent.Customer) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(customers))
	for i, c := range customers {
		opts[i] = templates.SelectOption{Value: c.ID, Label: c.DisplayName}
	}
	return opts
}

func statusID(j *ent.Job) int64 {
	if j.StatusID == nil {
		return 0
	}
	return *j.StatusID
}

func statusName(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Name
		}
	}
	return "Unknown"
}

func jobToDetail(j *ent.Job, statuses []*ent.Status) templates.JobDetail {
	d := templates.JobDetail{
		ID:          j.ID,
		CustomerID:  j.CustomerID,
		JobType:     j.JobType,
		Subtitle:    j.Subtitle,
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		BillingType: j.BillingType,
		Notes:       j.Notes,
		TechNotes:   j.TechNotes,
	}
	if !j.StartTime.IsZero() {
		d.StartTime = j.StartTime.Format("2006-01-02T15:04")
	}
	if !j.EndTime.IsZero() {
		d.EndTime = j.EndTime.Format("2006-01-02T15:04")
	}
	if !j.DueDate.IsZero() {
		d.DueDate = j.DueDate.Format("2006-01-02")
	}
	return d
}

func customerMap(customers []*ent.Customer) map[int64]string {
	m := make(map[int64]string, len(customers))
	for _, c := range customers {
		m[c.ID] = c.DisplayName
	}
	return m
}

func jobRow(j *ent.Job, statuses []*ent.Status, custMap map[int64]string) templates.JobRow {
	r := templates.JobRow{
		ID:          j.ID,
		DisplayName: j.JobType,
		JobType:     j.JobType,
		Customer:    custMap[j.CustomerID],
		StatusID:    statusID(j),
		StatusName:  statusName(statuses, j.StatusID),
		BillingType: j.BillingType,
	}
	if j.Subtitle != "" {
		r.DisplayName = j.JobType + " — " + j.Subtitle
	}
	if !j.StartTime.IsZero() {
		r.StartTime = j.StartTime.Format("Jan 2, 2006 3:04 PM")
	}
	return r
}

func parseTime(v string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04", strings.TrimSpace(v))
	return t
}

func parseDate(v string) time.Time {
	t, _ := time.Parse("2006-01-02", strings.TrimSpace(v))
	return t
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
