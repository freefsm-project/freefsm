package exports

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Documents exports the operational Estimate or Invoice list without amounts.
type Documents struct {
	ExportScope  authorization.Scope
	DocumentType string
}

func (d Documents) Kind() string               { return d.DocumentType + "-list-" + string(d.Scope()) + "-v1" }
func (d Documents) Permission() string         { return d.DocumentType + ".export" }
func (d Documents) Scope() authorization.Scope { return d.ExportScope }
func (Documents) Columns() []string {
	return []string{"as_of", "timezone", "document_id", "document_number", "title", "created_at", "status", "category", "job_id", "customer"}
}

func (d Documents) filter(raw json.RawMessage) (authorization.DocumentListFilter, error) {
	var f authorization.DocumentListFilter
	if err := decodeOperationalFilter(raw, &f); err != nil {
		return f, err
	}
	f, err := authorization.NormalizeDocumentListFilter(d.DocumentType, f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}
func (d Documents) Validate(raw json.RawMessage) error { _, err := d.filter(raw); return err }
func (d Documents) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	if (d.Scope() != authorization.Assigned && d.Scope() != authorization.All) || !p.Allows(d.Permission(), d.Scope()) || !p.Allows(d.DocumentType+".view", d.Scope()) {
		return authorization.ErrForbidden
	}
	return nil
}
func (d Documents) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	f, err := d.filter(raw)
	if err != nil {
		return err
	}
	return authorization.AuthorizeDocumentListFilter(ctx, tx, p, d.DocumentType, f, d.Scope(), true)
}
func (d Documents) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	f, err := d.filter(raw)
	if err != nil {
		return err
	}
	report, err := authorization.DocumentListSnapshot(ctx, tx, p, d.DocumentType, f, d.Scope(), true)
	if err != nil {
		return err
	}
	for _, row := range report.Rows {
		ref, err := json.Marshal(row.Reference)
		if err != nil {
			return err
		}
		values := []string{report.AsOf.Format(time.RFC3339Nano), report.Timezone, strconv.FormatInt(row.ID, 10), row.NumberLabel, row.Title, row.CreatedAt.Format(time.RFC3339Nano), row.StatusName, row.Category, optionalExportID(row.JobID), row.Customer}
		if err := emit(Record{Reference: ref, Values: values}); err != nil {
			return err
		}
	}
	return nil
}
func optionalExportID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
func (d Documents) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref authorization.DocumentListReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	return authorization.CheckDocumentListReference(ctx, tx, p, d.DocumentType, ref, d.Scope(), true)
}
