package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/freefsm-project/freefsm/internal/auditview"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Audit exports only the closed evidence projection, never arbitrary detail or
// retained removed text. Its entire artifact requires current sealed authority.
type Audit struct{}

func (Audit) Kind() string               { return "protected-audit-v1" }
func (Audit) Permission() string         { return "audit.export" }
func (Audit) Scope() authorization.Scope { return authorization.Sealed }
func (Audit) Columns() []string          { return auditview.Columns() }
func (Audit) Validate(raw json.RawMessage) error {
	var f auditview.Filters
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return ErrConflict
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrConflict
	}
	if err := f.Validate(); err != nil {
		return ErrConflict
	}
	return nil
}
func (Audit) Authorize(p authorization.Principal, _ json.RawMessage) error {
	if !p.Protected || !p.Allows("audit.view", authorization.Sealed) || !p.Allows("audit.export", authorization.Sealed) {
		return authorization.ErrForbidden
	}
	return nil
}
func (Audit) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := (Audit{}).Validate(raw); err != nil {
		return err
	}
	if err := (Audit{}).Authorize(p, raw); err != nil {
		return err
	}
	var f auditview.Filters
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	return auditview.ReadRows(ctx, tx, f, 0, 0, func(row auditview.Row) error {
		ref, _ := json.Marshal(struct {
			ID int64 `json:"id"`
		}{row.ID})
		return emit(Record{Values: row.Values, Reference: ref})
	})
}
func (Audit) Recheck(_ context.Context, _ pgx.Tx, p authorization.Principal, _ json.RawMessage) error {
	return (Audit{}).Authorize(p, nil)
}
