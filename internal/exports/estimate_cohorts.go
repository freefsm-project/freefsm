package exports

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// EstimateCohorts describes current state of original Estimate creation cohorts.
type EstimateCohorts struct{ ExportScope authorization.Scope }

func (d EstimateCohorts) Kind() string               { return "estimate-cohort-" + string(d.Scope()) + "-v1" }
func (EstimateCohorts) Permission() string           { return "estimate.export" }
func (d EstimateCohorts) Scope() authorization.Scope { return d.ExportScope }
func (EstimateCohorts) Columns() []string {
	return []string{"as_of", "timezone", "estimate_id", "estimate_number", "created_at", "status", "retained_category", "conversion_hidden", "active_conversion", "invoice_id", "invoice_number"}
}

func estimateCohortFilter(raw json.RawMessage) (authorization.EstimateCohortFilter, error) {
	var f authorization.EstimateCohortFilter
	if err := decodeOperationalFilter(raw, &f); err != nil {
		return f, err
	}
	f, err := authorization.NormalizeEstimateCohortFilter(f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}
func (EstimateCohorts) Validate(raw json.RawMessage) error {
	_, err := estimateCohortFilter(raw)
	return err
}
func (d EstimateCohorts) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	if d.Scope() != authorization.Assigned && d.Scope() != authorization.All {
		return authorization.ErrForbidden
	}
	for _, permission := range []string{"estimate.view", "estimate.export", "invoice.view", "invoice.export"} {
		if !p.Allows(permission, d.Scope()) {
			return authorization.ErrForbidden
		}
	}
	return nil
}
func (d EstimateCohorts) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	f, err := estimateCohortFilter(raw)
	if err != nil {
		return err
	}
	return authorization.AuthorizeEstimateCohortFilter(ctx, tx, p, f, d.Scope(), true)
}
func (d EstimateCohorts) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	f, err := estimateCohortFilter(raw)
	if err != nil {
		return err
	}
	report, err := authorization.EstimateCohortSnapshot(ctx, tx, p, f, d.Scope(), true)
	if err != nil {
		return err
	}
	for _, row := range report.Rows {
		ref, err := json.Marshal(row.Reference)
		if err != nil {
			return err
		}
		values := []string{report.AsOf.Format(time.RFC3339Nano), report.Timezone, strconv.FormatInt(row.EstimateID, 10), row.NumberLabel, row.CreatedAt.Format(time.RFC3339Nano), row.StatusName, row.Category, strconv.FormatBool(row.Hidden), strconv.FormatBool(row.ActiveConversion), optionalExportID(row.InvoiceID), row.InvoiceNumberLabel}
		if err := emit(Record{Reference: ref, Values: values}); err != nil {
			return err
		}
	}
	return nil
}
func (d EstimateCohorts) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref authorization.EstimateCohortReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	return authorization.CheckEstimateCohortReference(ctx, tx, p, ref, d.Scope(), true)
}
