package authorization

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrWorkloadFilter = errors.New("invalid Workload filter")

// Date bounds are inclusive company-local dates. Schedule selection always
// retains absent and repair schedules; Measure narrows the resulting population.
type WorkloadFilter struct {
	ScheduleFrom string  `json:"schedule_from"`
	ScheduleTo   string  `json:"schedule_to"`
	DueFrom      string  `json:"due_from"`
	DueTo        string  `json:"due_to"`
	CustomerID   int64   `json:"customer_id"`
	AssigneeIDs  []int64 `json:"assignee_ids"`
	Unassigned   bool    `json:"unassigned"`
	Category     string  `json:"category"`
	// Measure is a comma-separated conjunction, canonicalized in lexical order.
	// A drilldown adds a predicate to this set rather than replacing it.
	Measure string `json:"measure"`
	Sort    string `json:"sort"`
}

type WorkloadMeasures struct{ Total, Scheduled, Unscheduled, Repair, Overdue, Completed int }
type WorkloadRow struct {
	JobOperationalRow
	Scheduled, Unscheduled, Repair, Overdue, Completed bool
}
type WorkloadGroup struct {
	ID     int64
	Name   string
	Totals WorkloadMeasures
	Filter WorkloadFilter
}
type WorkloadReport struct {
	Filter            WorkloadFilter
	AsOf              time.Time
	Timezone          string
	Rows              []WorkloadRow
	Totals            WorkloadMeasures
	Groups            []WorkloadGroup
	GroupsNonadditive bool
}

func NormalizeWorkloadFilter(f WorkloadFilter) (WorkloadFilter, error) {
	if f.Sort == "" {
		f.Sort = "schedule_asc"
	}
	if f.Sort != "schedule_asc" || f.CustomerID < 0 || (f.Unassigned && len(f.AssigneeIDs) > 0) {
		return f, ErrWorkloadFilter
	}
	for _, d := range []string{f.ScheduleFrom, f.ScheduleTo, f.DueFrom, f.DueTo} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return f, ErrWorkloadFilter
			}
		}
	}
	if f.ScheduleFrom != "" && f.ScheduleTo != "" && f.ScheduleFrom > f.ScheduleTo || f.DueFrom != "" && f.DueTo != "" && f.DueFrom > f.DueTo {
		return f, ErrWorkloadFilter
	}
	if !slices.Contains([]string{"", "job:new", "job:travel_time", "job:in_progress", "job:pending", "job:completed", "job:canceled"}, f.Category) {
		return f, ErrWorkloadFilter
	}
	if f.Measure != "" {
		measures := strings.Split(f.Measure, ",")
		for _, measure := range measures {
			if !slices.Contains([]string{"scheduled", "unscheduled", "repair", "overdue", "completed"}, measure) {
				return f, ErrWorkloadFilter
			}
		}
		slices.Sort(measures)
		f.Measure = strings.Join(slices.Compact(measures), ",")
	}
	f.AssigneeIDs = slices.Clone(f.AssigneeIDs)
	slices.Sort(f.AssigneeIDs)
	f.AssigneeIDs = slices.Compact(f.AssigneeIDs)
	if len(f.AssigneeIDs) == 0 {
		f.AssigneeIDs = nil
	}
	for _, id := range f.AssigneeIDs {
		if id <= 0 {
			return f, ErrWorkloadFilter
		}
	}
	return f, nil
}

func CheckWorkloadFilter(ctx context.Context, tx pgx.Tx, p Principal, f WorkloadFilter, scope Scope) error {
	if _, err := NormalizeWorkloadFilter(f); err != nil {
		return err
	}
	if err := CheckJobOperationalFilter(ctx, tx, p, JobOperationalFilter{CustomerID: f.CustomerID}, scope); err != nil {
		return err
	}
	for _, id := range f.AssigneeIDs {
		var ok bool
		// Retained disabled participants remain selectable through visible Jobs.
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.company_id=$2 AND ($3 OR u.id=$4 OR EXISTS(SELECT 1 FROM job_assignments a JOIN jobs j ON j.id=a.job_id WHERE a.user_id=u.id AND j.company_id=$2 AND j.deleted_at IS NULL AND ($5 OR EXISTS(SELECT 1 FROM job_assignments own WHERE own.job_id=j.id AND own.user_id=$4)))))`, id, p.CompanyID, p.Allows("directory.view", All), p.UserID, scope == All).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

func WorkloadSnapshot(ctx context.Context, tx pgx.Tx, p Principal, filter WorkloadFilter, scope Scope) (WorkloadReport, error) {
	var out WorkloadReport
	f, err := NormalizeWorkloadFilter(filter)
	if err != nil {
		return out, err
	}
	if err = CheckWorkloadFilter(ctx, tx, p, f, scope); err != nil {
		return out, err
	}
	jobs, err := JobOperationalSnapshot(ctx, tx, p, JobOperationalFilter{CustomerID: f.CustomerID}, scope)
	if err != nil {
		return out, err
	}
	out = WorkloadReport{Filter: f, AsOf: jobs.AsOf, Timezone: jobs.Timezone, GroupsNonadditive: true}
	loc, err := time.LoadLocation(out.Timezone)
	if err != nil {
		return WorkloadReport{}, err
	}
	groups := map[int64]*WorkloadGroup{}
	for _, job := range jobs.Rows {
		r, match := workloadClassify(job, f, out.AsOf, loc)
		if !match {
			continue
		}
		out.Rows = append(out.Rows, r)
		addWorkloadMeasures(&out.Totals, r)
		ids, names := job.AssigneeIDs, job.Assignees
		if len(ids) == 0 {
			ids = []int64{0}
			names = []string{"Unassigned"}
		}
		for i, id := range ids {
			if len(f.AssigneeIDs) > 0 && !slices.Contains(f.AssigneeIDs, id) {
				continue
			}
			g := groups[id]
			if g == nil {
				gf := f
				gf.AssigneeIDs = nil
				gf.Unassigned = id == 0
				if id != 0 {
					gf.AssigneeIDs = []int64{id}
				}
				g = &WorkloadGroup{ID: id, Name: names[i], Filter: gf}
				groups[id] = g
			}
			addWorkloadMeasures(&g.Totals, r)
		}
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Start == nil || b.Start == nil {
			if (a.Start == nil) != (b.Start == nil) {
				return b.Start == nil
			}
		} else if !a.Start.Equal(*b.Start) {
			return a.Start.Before(*b.Start)
		}
		return a.ID < b.ID
	})
	for _, g := range groups {
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out, nil
}

func workloadClassify(j JobOperationalRow, f WorkloadFilter, asOf time.Time, loc *time.Location) (WorkloadRow, bool) {
	r := WorkloadRow{JobOperationalRow: j}
	r.Unscheduled = j.Start == nil && j.End == nil && j.ArrivalStart == nil && j.ArrivalEnd == nil
	valid := j.Start != nil && j.End != nil && j.Start.Before(*j.End)
	if (j.ArrivalStart == nil) != (j.ArrivalEnd == nil) {
		valid = false
	}
	if j.ArrivalStart != nil && j.ArrivalEnd != nil && (j.Start == nil || j.ArrivalStart.After(*j.Start) || j.ArrivalEnd.Before(*j.Start) || j.ArrivalEnd.Before(*j.ArrivalStart)) {
		valid = false
	}
	r.Repair = !valid && !r.Unscheduled
	r.Scheduled = valid
	if valid {
		if f.ScheduleFrom != "" {
			start, _ := time.ParseInLocation("2006-01-02", f.ScheduleFrom, loc)
			if !j.End.After(start) {
				return r, false
			}
		}
		if f.ScheduleTo != "" {
			end, _ := time.ParseInLocation("2006-01-02", f.ScheduleTo, loc)
			if !j.Start.Before(end.AddDate(0, 0, 1)) {
				return r, false
			}
		}
	}
	if f.DueFrom != "" || f.DueTo != "" {
		if j.Due == nil {
			return r, false
		}
		due := j.Due.Format("2006-01-02")
		if f.DueFrom != "" && due < f.DueFrom || f.DueTo != "" && due > f.DueTo {
			return r, false
		}
	}
	if f.Category != "" && j.Category != f.Category || f.Unassigned && len(j.AssigneeIDs) != 0 {
		return r, false
	}
	if len(f.AssigneeIDs) > 0 {
		found := false
		for _, id := range j.AssigneeIDs {
			found = found || slices.Contains(f.AssigneeIDs, id)
		}
		if !found {
			return r, false
		}
	}
	r.Completed = j.Category == "job:completed"
	r.Overdue = j.Due != nil && j.Due.Format("2006-01-02") < asOf.In(loc).Format("2006-01-02") && !r.Completed && j.Category != "job:canceled"
	if f.Measure != "" {
		for _, measure := range strings.Split(f.Measure, ",") {
			match := measure == "scheduled" && r.Scheduled || measure == "unscheduled" && r.Unscheduled || measure == "repair" && r.Repair || measure == "overdue" && r.Overdue || measure == "completed" && r.Completed
			if !match {
				return r, false
			}
		}
	}
	return r, true
}

func addWorkloadMeasures(m *WorkloadMeasures, r WorkloadRow) {
	m.Total++
	if r.Scheduled {
		m.Scheduled++
	}
	if r.Unscheduled {
		m.Unscheduled++
	}
	if r.Repair {
		m.Repair++
	}
	if r.Overdue {
		m.Overdue++
	}
	if r.Completed {
		m.Completed++
	}
}

type WorkloadReference = JobOperationalReference

func CheckWorkloadReference(ctx context.Context, tx pgx.Tx, p Principal, ref WorkloadReference, scope Scope) error {
	return CheckJobOperationalReference(ctx, tx, p, ref, scope)
}

func (s *Service) Workload(ctx context.Context, a Actor, f WorkloadFilter, scope Scope) (WorkloadReport, error) {
	var out WorkloadReport
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	err := s.WithinSnapshotPermission(ctx, a, "job.view", scope, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = WorkloadSnapshot(ctx, tx, p, f, scope)
		return err
	})
	if err != nil {
		return WorkloadReport{}, err
	}
	return out, nil
}
