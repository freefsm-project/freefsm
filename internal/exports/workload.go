package exports

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Workload emits distinct Jobs, not nonadditive per-assignee report groups.
type Workload struct{ ExportScope authorization.Scope }

func (d Workload) Kind() string               { return "workload-" + string(d.Scope()) + "-v1" }
func (Workload) Permission() string           { return "job.export" }
func (d Workload) Scope() authorization.Scope { return d.ExportScope }
func (Workload) Columns() []string {
	return []string{"as_of", "timezone", "job_id", "title", "status", "category", "customer", "assignees", "schedule_start", "schedule_end", "due_date", "scheduled", "unscheduled", "repair", "overdue", "completed"}
}

func workloadFilter(raw json.RawMessage) (authorization.WorkloadFilter, error) {
	var f authorization.WorkloadFilter
	if err := decodeOperationalFilter(raw, &f); err != nil {
		return f, err
	}
	f, err := authorization.NormalizeWorkloadFilter(f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}
func (Workload) Validate(raw json.RawMessage) error { _, err := workloadFilter(raw); return err }
func (d Workload) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	return authorization.AuthorizeJobOperationalExport(p, d.Scope())
}
func (d Workload) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	f, err := workloadFilter(raw)
	if err != nil {
		return err
	}
	return authorization.CheckWorkloadFilter(ctx, tx, p, f, d.Scope())
}
func (d Workload) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	f, err := workloadFilter(raw)
	if err != nil {
		return err
	}
	report, err := authorization.WorkloadSnapshot(ctx, tx, p, f, d.Scope())
	if err != nil {
		return err
	}
	for _, row := range report.Rows {
		ref, err := json.Marshal(row.Reference)
		if err != nil {
			return err
		}
		values := operationalJobValues(report.AsOf, report.Timezone, row.JobOperationalRow)
		values = append(values, strconv.FormatBool(row.Scheduled), strconv.FormatBool(row.Unscheduled), strconv.FormatBool(row.Repair), strconv.FormatBool(row.Overdue), strconv.FormatBool(row.Completed))
		if err := emit(Record{Reference: ref, Values: values}); err != nil {
			return err
		}
	}
	return nil
}
func (d Workload) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref authorization.WorkloadReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	return authorization.CheckWorkloadReference(ctx, tx, p, ref, d.Scope())
}
