package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Jobs is the fixed operational Job projection; it has no pricing columns.
type Jobs struct{ ExportScope authorization.Scope }

func (d Jobs) Kind() string               { return "job-operational-" + string(d.Scope()) + "-v1" }
func (Jobs) Permission() string           { return "job.export" }
func (d Jobs) Scope() authorization.Scope { return d.ExportScope }
func (Jobs) Columns() []string {
	return []string{"as_of", "timezone", "job_id", "title", "status", "category", "customer", "assignees", "schedule_start", "schedule_end", "due_date"}
}

func jobOperationalFilter(raw json.RawMessage) (authorization.JobOperationalFilter, error) {
	var f authorization.JobOperationalFilter
	if err := decodeOperationalFilter(raw, &f); err != nil {
		return f, err
	}
	f, err := authorization.NormalizeJobOperationalFilter(f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}

// Filter decoding is strict; the domain normalizers own filter semantics.
func decodeOperationalFilter(raw json.RawMessage, target any) error {
	if !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrConflict
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrConflict
	}
	return nil
}

func (Jobs) Validate(raw json.RawMessage) error { _, err := jobOperationalFilter(raw); return err }
func (d Jobs) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	return authorization.AuthorizeJobOperationalExport(p, d.Scope())
}
func (d Jobs) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	f, err := jobOperationalFilter(raw)
	if err != nil {
		return err
	}
	return authorization.CheckJobOperationalFilter(ctx, tx, p, f, d.Scope())
}
func (d Jobs) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	f, err := jobOperationalFilter(raw)
	if err != nil {
		return err
	}
	report, err := authorization.JobOperationalSnapshot(ctx, tx, p, f, d.Scope())
	if err != nil {
		return err
	}
	for _, row := range report.Rows {
		ref, err := json.Marshal(row.Reference)
		if err != nil {
			return err
		}
		if err := emit(Record{Reference: ref, Values: operationalJobValues(report.AsOf, report.Timezone, row)}); err != nil {
			return err
		}
	}
	return nil
}
func operationalJobValues(asOf time.Time, zone string, row authorization.JobOperationalRow) []string {
	assignees := strings.Join(row.Assignees, "; ")
	if len(row.Assignees) == 0 {
		assignees = "Unassigned"
	}
	due := ""
	if row.Due != nil {
		due = row.Due.Format("2006-01-02")
	}
	return []string{asOf.Format(time.RFC3339Nano), zone, strconv.FormatInt(row.ID, 10), row.Title, row.Status, row.Category, row.Customer, assignees, exportInstant(row.Start), exportInstant(row.End), due}
}
func (d Jobs) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref authorization.JobOperationalReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	return authorization.CheckJobOperationalReference(ctx, tx, p, ref, d.Scope())
}
