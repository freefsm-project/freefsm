package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

var errWorkReportFilter = errors.New("invalid report filters")

func selectedReportID(q url.Values, key string) (int64, error) {
	v := q.Get(key)
	if v == "" || v == "0" {
		return 0, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errWorkReportFilter
	}
	return id, nil
}

func workReportScope(r *http.Request, permission string) (authorization.Scope, []string, error) {
	auth, ok := authorization.FromContext(r.Context())
	if !ok || auth.Actor.Channel == "api" {
		return "", nil, authorization.ErrForbidden
	}
	p, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		return "", nil, err
	}
	var scopes []string
	for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned} {
		if p.Allows(permission, scope) {
			scopes = append(scopes, string(scope))
		}
	}
	scope := authorization.Scope(r.FormValue("scope"))
	if scope == "" && len(scopes) > 0 {
		scope = authorization.Scope(scopes[0])
	}
	if (scope != authorization.All && scope != authorization.Assigned) || !p.Allows(permission, scope) {
		return "", scopes, authorization.ErrForbidden
	}
	return scope, scopes, nil
}

func parseJobOperationalFilter(q url.Values) (authorization.JobOperationalFilter, error) {
	f := authorization.JobOperationalFilter{Search: q.Get("search"), Sort: q.Get("sort")}
	var err error
	if f.StatusID, err = selectedReportID(q, "status_id"); err != nil {
		return f, err
	}
	if f.CustomerID, err = selectedReportID(q, "customer_id"); err != nil {
		return f, err
	}
	return authorization.NormalizeJobOperationalFilter(f)
}

func parseDocumentListFilter(kind string, q url.Values) (authorization.DocumentListFilter, error) {
	f := authorization.DocumentListFilter{Search: q.Get("search"), Sort: q.Get("sort"), Valid: q.Get("valid")}
	var err error
	if f.StatusID, err = selectedReportID(q, "status_id"); err != nil {
		return f, err
	}
	if f.CustomerID, err = selectedReportID(q, "customer_id"); err != nil {
		return f, err
	}
	return authorization.NormalizeDocumentListFilter(kind, f)
}

func parseWorkloadFilter(q url.Values) (authorization.WorkloadFilter, error) {
	f := authorization.WorkloadFilter{ScheduleFrom: q.Get("schedule_from"), ScheduleTo: q.Get("schedule_to"), DueFrom: q.Get("due_from"), DueTo: q.Get("due_to"), Category: q.Get("category"), Measure: q.Get("measure"), Sort: q.Get("sort"), Unassigned: q.Get("unassigned") == "true"}
	if v := q.Get("unassigned"); v != "" && v != "true" && v != "false" {
		return f, errWorkReportFilter
	}
	var err error
	if f.CustomerID, err = selectedReportID(q, "customer_id"); err != nil {
		return f, err
	}
	for _, value := range q["assignee_id"] {
		for _, raw := range strings.Split(value, ",") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || id <= 0 {
				return f, errWorkReportFilter
			}
			f.AssigneeIDs = append(f.AssigneeIDs, id)
		}
	}
	return authorization.NormalizeWorkloadFilter(f)
}

func parseEstimateCohortFilter(q url.Values) (authorization.EstimateCohortFilter, error) {
	f := authorization.EstimateCohortFilter{CreatedFrom: q.Get("created_from"), CreatedTo: q.Get("created_to"), Category: q.Get("category"), Conversion: q.Get("conversion"), Sort: q.Get("sort")}
	var err error
	if f.CustomerID, err = selectedReportID(q, "customer_id"); err != nil {
		return f, err
	}
	return authorization.NormalizeEstimateCohortFilter(f)
}

func workloadValues(f authorization.WorkloadFilter, scope authorization.Scope) url.Values {
	q := url.Values{"scope": {string(scope)}, "sort": {f.Sort}, "schedule_from": {f.ScheduleFrom}, "schedule_to": {f.ScheduleTo}, "due_from": {f.DueFrom}, "due_to": {f.DueTo}, "category": {f.Category}, "measure": {f.Measure}, "unassigned": {strconv.FormatBool(f.Unassigned)}}
	if f.CustomerID != 0 {
		q.Set("customer_id", strconv.FormatInt(f.CustomerID, 10))
	}
	var ids []string
	for _, id := range f.AssigneeIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	q.Set("assignee_id", strings.Join(ids, ","))
	return q
}

func cohortValues(f authorization.EstimateCohortFilter, scope authorization.Scope) url.Values {
	q := url.Values{"scope": {string(scope)}, "sort": {f.Sort}, "created_from": {f.CreatedFrom}, "created_to": {f.CreatedTo}, "category": {f.Category}, "conversion": {f.Conversion}}
	if f.CustomerID != 0 {
		q.Set("customer_id", strconv.FormatInt(f.CustomerID, 10))
	}
	return q
}

func documentListValues(f authorization.DocumentListFilter, scope authorization.Scope) url.Values {
	q := url.Values{"scope": {string(scope)}, "sort": {f.Sort}, "search": {f.Search}}
	if f.StatusID != 0 {
		q.Set("status_id", strconv.FormatInt(f.StatusID, 10))
	}
	if f.CustomerID != 0 {
		q.Set("customer_id", strconv.FormatInt(f.CustomerID, 10))
	}
	if f.Valid != "" {
		q.Set("valid", f.Valid)
	}
	return q
}

func reportPageBounds(r *http.Request, count int) (page, start, end int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	// Bound before multiplying so arbitrarily large page values cannot overflow.
	if page > count/25+1 {
		page = count/25 + 1
	}
	start = (page - 1) * 25
	end = min(start+25, count)
	return
}
