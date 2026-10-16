package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Timesheets is a versioned adapter to the shared durable export engine.
type Timesheets struct {
	ExportScope authorization.Scope
	GPS         bool
}

func (d Timesheets) Kind() string {
	if d.GPS {
		return "clock-gps-" + string(d.Scope()) + "-v1"
	}
	return "timesheet-" + string(d.Scope()) + "-v1"
}
func (d Timesheets) Permission() string {
	if d.GPS {
		return "time.gps.export"
	}
	return "time.export"
}
func (d Timesheets) Scope() authorization.Scope { return d.ExportScope }
func (d Timesheets) Columns() []string {
	if d.GPS {
		return []string{"as_of", "timezone", "grouping", "group_id", "group_name", "entry_id", "subject_id", "subject", "job_id", "job_title", "source", "lifecycle", "clock_event", "latitude", "longitude", "captured_at", "accuracy"}
	}
	return []string{"as_of", "timezone", "grouping", "group_id", "group_name", "entry_id", "subject_id", "subject", "job_id", "job_title", "source", "lifecycle", "clock_in", "clock_out", "company_date", "duration_microseconds", "provisional", "notes", "revisions"}
}
func (Timesheets) Validate(raw json.RawMessage) error {
	_, err := timeExportFilter(raw)
	return err
}
func timeExportFilter(raw json.RawMessage) (authorization.TimesheetFilter, error) {
	var f authorization.TimesheetFilter
	if !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return f, ErrConflict
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, ErrConflict
	}
	f, err := authorization.NormalizeTimesheetFilter(f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}
func (d Timesheets) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	return authorization.AuthorizeTimeExport(p, d.Permission(), d.Scope())
}
func (d Timesheets) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	f, err := timeExportFilter(raw)
	if err != nil {
		return err
	}
	report, err := authorization.TimeExportSnapshot(ctx, tx, p, f, d.Permission(), d.Scope())
	if err != nil {
		return err
	}
	for _, row := range report.Rows {
		ref, err := json.Marshal(timeExportReference(row, !d.GPS))
		if err != nil {
			return err
		}
		if d.GPS {
			for _, fact := range row.GPS {
				accuracy := ""
				if fact.Accuracy != nil {
					accuracy = strconv.FormatFloat(*fact.Accuracy, 'f', -1, 64)
				}
				values := append(timeExportIdentity(report, row), fact.Event, strconv.FormatFloat(fact.Latitude, 'f', -1, 64), strconv.FormatFloat(fact.Longitude, 'f', -1, 64), fact.CapturedAt.Format(time.RFC3339Nano), accuracy)
				if err := emit(Record{Reference: ref, Values: values}); err != nil {
					return err
				}
			}
			continue
		}
		history, err := json.Marshal(row.History)
		if err != nil {
			return err
		}
		days := row.Days
		if len(days) == 0 {
			days = []authorization.TimesheetDay{{}}
		}
		for _, day := range days {
			values := timeExportIdentity(report, row)
			values = append(values, row.Start.Format(time.RFC3339Nano), exportInstant(row.End), day.Date, strconv.FormatInt(int64(day.Duration), 10), strconv.FormatBool(row.Provisional), row.Notes, string(history))
			if err := emit(Record{Reference: ref, Values: values}); err != nil {
				return err
			}
		}
	}
	return nil
}
func timeExportIdentity(report authorization.Timesheet, row authorization.TimesheetRow) []string {
	source, lifecycle := "clocked", "closed"
	if row.Manual {
		source = "manual"
	}
	if row.Access.VoidedAt != nil {
		lifecycle = "void"
	} else if row.End == nil {
		lifecycle = "active"
	}
	job := ""
	if row.Access.JobID != 0 {
		job = fmt.Sprint(row.Access.JobID)
	}
	return []string{report.AsOf.Format(time.RFC3339Nano), report.Timezone, report.Filter.GroupBy, fmt.Sprint(row.GroupID), row.GroupLabel, fmt.Sprint(row.ID), fmt.Sprint(row.Access.UserID), row.Access.SubjectName, job, row.Access.JobTitle, source, lifecycle}
}
func exportInstant(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
func timeExportReference(row authorization.TimesheetRow, history bool) authorization.TimeExportReference {
	ref := authorization.TimeExportReference{EntryID: row.ID, SubjectID: row.Access.UserID, JobID: row.Access.JobID}
	if row.Access.JobTitle != "" && row.Access.JobID != 0 {
		ref.TitleJobs = append(ref.TitleJobs, row.Access.JobID)
	}
	if history {
		for _, revision := range row.History {
			for _, state := range []*authorization.TimeState{revision.Before, &revision.After} {
				if state != nil && state.JobTitle != "" && state.JobID != nil {
					ref.TitleJobs = append(ref.TitleJobs, *state.JobID)
				}
			}
		}
	}
	return ref
}
func (d Timesheets) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref authorization.TimeExportReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	return authorization.CheckTimeExportReference(ctx, tx, p, ref, d.Permission(), d.Scope())
}
