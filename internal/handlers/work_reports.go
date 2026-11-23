package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

const workloadPath = "/reports/workload"
const cohortPath = "/reports/estimate-cohort"
const cohortDescription = "Current state of the authorized current active/retained-conversion Estimate creation cohort. Original Estimate creation dates select the cohort; this is not the entire historical population or historical as-of stages. Archived records are excluded. Retained category and active conversion are separate measures; reverted conversions do not count."

func registerWorkReports(r chi.Router) {
	r.Get(workloadPath, workloadReport)
	r.Get(cohortPath, estimateCohortReport)
}

func reportOptions(values ...string) []templates.WorkReportOption {
	out := make([]templates.WorkReportOption, 0, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		out = append(out, templates.WorkReportOption{Value: values[i], Label: values[i+1]})
	}
	return out
}

func workloadFields() []templates.WorkReportField {
	return []templates.WorkReportField{
		{Name: "schedule_from", Label: "Schedule from (inclusive company date)", Type: "date"},
		{Name: "schedule_to", Label: "Schedule through (inclusive company date)", Type: "date"},
		{Name: "due_from", Label: "Due from", Type: "date"}, {Name: "due_to", Label: "Due through", Type: "date"},
		{Name: "customer_id", Label: "Customer ID", Type: "number"},
		{Name: "assignee_id", Label: "Assignee IDs (comma-separated; any selected assignee)", Type: "text"},
		{Name: "unassigned", Label: "Assignment", Options: reportOptions("false", "Any assignment", "true", "Unassigned only")},
		{Name: "category", Label: "Current category", Options: reportOptions("", "All categories", "job:new", "New", "job:travel_time", "Travel Time", "job:in_progress", "In Progress", "job:pending", "Pending", "job:completed", "Completed", "job:canceled", "Canceled")},
		{Name: "measure", Label: "Measure", Options: reportOptions("", "All matching Jobs", "scheduled", "Scheduled", "unscheduled", "Unscheduled", "repair", "Schedule repair backlog", "overdue", "Overdue", "completed", "Completed")},
		{Name: "sort", Label: "Order", Options: reportOptions("schedule_asc", "Schedule ascending, then Job ID")},
	}
}

func cohortFields() []templates.WorkReportField {
	return []templates.WorkReportField{
		{Name: "created_from", Label: "Created from (inclusive company date)", Type: "date"}, {Name: "created_to", Label: "Created through (inclusive company date)", Type: "date"},
		{Name: "customer_id", Label: "Customer ID", Type: "number"},
		{Name: "category", Label: "Current / retained Estimate category", Options: reportOptions("", "All categories", "estimate:draft", "Draft", "estimate:estimate", "Estimate", "estimate:sent", "Sent", "estimate:accepted", "Accepted", "estimate:rejected", "Rejected", "estimate:completed", "Completed")},
		{Name: "conversion", Label: "Active conversion (separate from category)", Options: reportOptions("", "All authorized states", "active", "Active conversion", "none", "No active conversion")},
		{Name: "sort", Label: "Order", Options: documentSortOptions()},
	}
}

func documentSortOptions() []templates.WorkReportOption {
	return reportOptions("created_desc", "Newest creation, then ID descending", "created_asc", "Oldest creation, then ID", "title_asc", "Title, then ID", "number_asc", "Number, then ID")
}

func reportReadError(w http.ResponseWriter, r *http.Request, p templates.WorkReportPageData, err error) {
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	slog.Warn("work report unavailable", "path", r.URL.Path, "error", err)
	p.Error = "The current report could not be loaded. Your filters are retained; retry to obtain a fresh snapshot."
	if len(p.Scopes) == 0 {
		p.Scopes = []string{"A", "J"}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	templates.WorkReport(p).Render(r.Context(), w)
}

func reportLink(path string, q url.Values, key, value string) string {
	copy := url.Values{}
	for k, v := range q {
		copy[k] = append([]string(nil), v...)
	}
	if key != "" {
		copy.Set(key, value)
	}
	return path + "?" + copy.Encode()
}

func workloadMeasures(t authorization.WorkloadMeasures, q url.Values) []templates.WorkReportMeasure {
	var out []templates.WorkReportMeasure
	for _, m := range []struct {
		label, key string
		n          int
	}{{"Distinct Jobs", "", t.Total}, {"Scheduled", "scheduled", t.Scheduled}, {"Unscheduled", "unscheduled", t.Unscheduled}, {"Schedule repair backlog", "repair", t.Repair}, {"Overdue", "overdue", t.Overdue}, {"Completed", "completed", t.Completed}} {
		// Total drills into the exact current population, including its measure.
		link := reportLink(workloadPath, q, "", "")
		if m.key != "" {
			link = reportLink(workloadPath, q, "measure", intersectWorkloadMeasure(q.Get("measure"), m.key))
		}
		out = append(out, templates.WorkReportMeasure{Label: m.label, Value: strconv.Itoa(m.n), URL: link})
	}
	return out
}

func intersectWorkloadMeasure(current, added string) string {
	seen := map[string]bool{}
	for _, value := range strings.Split(current, ",") {
		if value != "" {
			seen[value] = true
		}
	}
	seen[added] = true
	var values []string
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func snapshotStamp(at time.Time, zone string) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return at.Format(time.RFC3339)
	}
	return at.In(loc).Format(time.RFC3339)
}

func reportInstant(t *time.Time, zone string) string {
	if t == nil {
		return "Not scheduled"
	}
	return snapshotStamp(*t, zone)
}

func workloadReport(w http.ResponseWriter, r *http.Request) {
	p := templates.WorkReportPageData{Title: "Workload", Path: workloadPath, Description: "Current active Jobs only. Scheduled intervals intersect the selected company dates; unscheduled and incomplete schedules remain explicit backlog. Overdue means due before today in the company timezone, excluding Completed and Canceled.", Filters: r.URL.Query(), Fields: workloadFields(), Nonadditive: true}
	f, err := parseWorkloadFilter(r.URL.Query())
	if err != nil {
		http.Error(w, "Invalid report filters", 400)
		return
	}
	p.Filters.Set("measure", f.Measure)
	if strings.Contains(f.Measure, ",") {
		for i := range p.Fields {
			if p.Fields[i].Name == "measure" {
				p.Fields[i].Options = append(p.Fields[i].Options, templates.WorkReportOption{Value: f.Measure, Label: "All of: " + f.Measure})
			}
		}
	}
	scope, scopes, err := workReportScope(r, "job.view")
	p.Scopes = scopes
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	p.Filters = workloadValues(f, scope)
	auth, _ := authorization.FromContext(r.Context())
	report, err := auth.Service.Workload(r.Context(), auth.Actor, f, scope)
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	p.Filters = workloadValues(report.Filter, scope)
	p.UpdatedAt, p.Timezone = snapshotStamp(report.AsOf, report.Timezone), report.Timezone
	p.Summary = workloadMeasures(report.Totals, p.Filters)
	for _, group := range report.Groups {
		p.Groups = append(p.Groups, templates.WorkReportGroup{Label: group.Name, Measures: workloadMeasures(group.Totals, workloadValues(group.Filter, scope))})
	}
	p.Headers = []string{"Job", "Customer", "Status / category", "Scheduled start", "Scheduled end", "Due date", "All assignees", "Measures"}
	for _, row := range report.Rows {
		label := row.Title
		if row.Subtitle != "" {
			label += " — " + row.Subtitle
		}
		due := "No due date"
		if row.Due != nil {
			due = row.Due.Format("2006-01-02")
		}
		assignees := strings.Join(row.Assignees, ", ")
		if assignees == "" {
			assignees = "Unassigned"
		}
		var flags []string
		for _, flag := range []struct {
			on   bool
			text string
		}{{row.Scheduled, "Scheduled"}, {row.Unscheduled, "Unscheduled"}, {row.Repair, "Schedule repair required"}, {row.Overdue, "Overdue"}, {row.Completed, "Completed"}} {
			if flag.on {
				flags = append(flags, flag.text)
			}
		}
		p.Rows = append(p.Rows, templates.WorkReportRow{Label: label, URL: fmt.Sprintf("/jobs/%d", row.ID), Cells: []string{row.Customer, row.Status + " / " + row.Category, reportInstant(row.Start, report.Timezone), reportInstant(row.End, report.Timezone), due, assignees, strings.Join(flags, ", ")}})
	}
	principal, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	if authorization.AuthorizeJobOperationalExport(principal, scope) == nil {
		p.ExportURL = reportLink(workloadPath+"/export", p.Filters, "", "")
	}
	w.Header().Set("Cache-Control", "no-store")
	templates.WorkReport(p).Render(r.Context(), w)
}

func estimateCohortReport(w http.ResponseWriter, r *http.Request) {
	p := templates.WorkReportPageData{Title: "Estimate creation cohort", Path: cohortPath, Description: cohortDescription, Filters: r.URL.Query(), Fields: cohortFields()}
	f, err := parseEstimateCohortFilter(r.URL.Query())
	if err != nil {
		http.Error(w, "Invalid report filters", 400)
		return
	}
	scope, scopes, err := workReportScope(r, "estimate.view")
	p.Scopes = scopes
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	p.Filters = cohortValues(f, scope)
	auth, _ := authorization.FromContext(r.Context())
	report, err := auth.Service.EstimateCohort(r.Context(), auth.Actor, f, scope)
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	p.Filters = cohortValues(report.Filter, scope)
	p.UpdatedAt, p.Timezone = snapshotStamp(report.AsOf, report.Timezone), report.Timezone
	p.Summary = []templates.WorkReportMeasure{{Label: "Distinct Estimates", Value: strconv.Itoa(report.Totals.Total), URL: reportLink(cohortPath, p.Filters, "", "")}}
	principal, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		reportReadError(w, r, p, err)
		return
	}
	if principal.Allows("invoice.view", scope) {
		link := reportLink(cohortPath, p.Filters, "conversion", "active")
		if report.Filter.Conversion == "none" {
			link = ""
		}
		p.Summary = append(p.Summary, templates.WorkReportMeasure{Label: "Active conversions (separate from category)", Value: strconv.Itoa(report.Totals.ActiveConversions), URL: link})
	}
	var categories []string
	for category := range report.Totals.Categories {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	for _, category := range categories {
		p.Summary = append(p.Summary, templates.WorkReportMeasure{Label: "Current / retained category: " + category, Value: strconv.Itoa(report.Totals.Categories[category]), URL: reportLink(cohortPath, p.Filters, "category", category)})
	}
	p.Headers = []string{"Estimate", "Original creation", "Current / retained category", "Status label", "Active conversion", "Source visibility"}
	p.RelatedHeader = "Authorized active Invoice"
	for _, row := range report.Rows {
		visibility := "Active"
		if row.Hidden {
			visibility = "Retained conversion source — no ordinary detail access"
		}
		conversion := "No active conversion"
		if row.ActiveConversion {
			conversion = "Active conversion: " + row.InvoiceNumberLabel
		}
		p.Rows = append(p.Rows, templates.WorkReportRow{Label: row.NumberLabel + " — " + row.Title, URL: row.EstimateURL, RelatedLabel: "Active Invoice " + row.InvoiceNumberLabel, RelatedURL: row.InvoiceURL, Cells: []string{snapshotStamp(row.CreatedAt, report.Timezone), row.Category, row.StatusName, conversion, visibility}})
	}
	if principal.Allows("estimate.export", scope) && principal.Allows("invoice.export", scope) && principal.Allows("invoice.view", scope) {
		p.ExportURL = reportLink(cohortPath+"/export", p.Filters, "", "")
	}
	w.Header().Set("Cache-Control", "no-store")
	templates.WorkReport(p).Render(r.Context(), w)
}
