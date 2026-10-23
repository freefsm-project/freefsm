package conversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/saveaction"
)

type Preview struct {
	InvoiceDate   string          `json:"invoice_date"`
	DueDate       *string         `json:"due_date"`
	ID            int64           `json:"id"`
	JobID         *int64          `json:"job_id"`
	Revision      string          `json:"revision"`
	Title         string          `json:"title"`
	Notes         string          `json:"notes"`
	Terms         *string         `json:"terms"`
	LineItems     json.RawMessage `json:"line_items,omitempty"`
	TaxRate       string          `json:"tax_rate,omitempty"`
	Financial     json.RawMessage `json:"financial,omitempty"`
	SourceFields  json.RawMessage `json:"source_fields"`
	InvoiceFields json.RawMessage `json:"invoice_fields,omitempty"`
	Definitions   json.RawMessage `json:"definitions"`
	CanEdit       bool            `json:"can_edit"`
	Validation    string          `json:"validation,omitempty"`
}

// Preview acquires the same authority/configuration fence as commit, but never
// allocates an identity, number, receipt, cycle or relation. Invalid values are
// displayed for review without executing any commercial effects.
func (s *Service) Preview(ctx context.Context, id int64, input *json.RawMessage) (Preview, error) {
	out := Preview{ID: id}
	bound, ok := authorization.FromContext(ctx)
	if !ok {
		return out, authorization.ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	approval, err := bound.Service.AuthorizeConversion(ctx, tx, bound.Actor, id)
	if err != nil {
		return out, err
	}
	out.CanEdit = bound.Service.AuthorizeConversionValues(ctx, tx, approval) == nil
	if input != nil && !out.CanEdit {
		return out, authorization.ErrForbidden
	}
	var company int64
	if err = tx.QueryRow(ctx, `SELECT company_id,job_id,title,notes,terms,custom_fields FROM estimates WHERE id=$1 AND conversion_hidden_at IS NULL`, id).Scan(&company, &out.JobID, &out.Title, &out.Notes, &out.Terms, &out.SourceFields); err != nil {
		return out, authorization.ErrNotFound
	}
	out.Revision, err = saveaction.Revision(ctx, tx, bound.Actor.UserID, fmt.Sprintf("/estimates/%d", id))
	if err != nil {
		return out, err
	}
	var timezone string
	var dueDays *int
	var dueOverride *string
	if err = tx.QueryRow(ctx, `SELECT s.timezone,s.default_due_days,d.defaults->>'invoice_due_days' FROM estimates e JOIN company_settings s ON s.company_id=e.company_id LEFT JOIN customer_document_defaults d ON d.customer_id=e.customer_id WHERE e.id=$1`, id).Scan(&timezone, &dueDays, &dueOverride); err != nil {
		return out, err
	}
	date, due, err := invoiceDates(s.now(), timezone, dueDays, dueOverride)
	if err != nil {
		return out, err
	}
	out.InvoiceDate = date.Format("2006-01-02")
	if !due.IsZero() {
		formatted := due.Format("2006-01-02")
		out.DueDate = &formatted
	}
	if err = tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(d) || jsonb_build_object('options',d.options::text,'retired_options',d.retired_options::text) ORDER BY id),'[]') FROM custom_field_definitions d WHERE custom_field_applies_to_company(company_id,$1) AND object_type IN ('estimate','invoice')`, company).Scan(&out.Definitions); err != nil {
		return out, err
	}
	if bound.Service.ParentFacet(ctx, bound.Actor, "estimate", id, "pricing.view") == nil {
		var financial string
		if err = tx.QueryRow(ctx, `SELECT line_items,tax_rate::text,financial FROM estimates WHERE id=$1`, id).Scan(&out.LineItems, &out.TaxRate, &financial); err != nil {
			return out, err
		}
		if financial != "" {
			out.Financial = json.RawMessage(financial)
		}
	}
	var fields *string
	if input != nil {
		raw := string(*input)
		fields = &raw
	}
	merged, _, err := invoiceFieldInput(ctx, tx, company, out.SourceFields, fields)
	if err == nil {
		out.InvoiceFields = json.RawMessage(merged)
		var validated string
		validated, err = customfields.Validate(ctx, tx, "invoice", "[]", &merged, customfields.Creation, company)
		if err == nil {
			out.InvoiceFields = json.RawMessage(validated)
		}
	}
	var field *customfields.FieldError
	if errors.As(err, &field) {
		out.Validation = field.Error()
		return out, nil
	}
	return out, err
}
