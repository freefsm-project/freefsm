package authorization

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// DocumentCreationInputs is a purpose-limited preparation projection, never a
// Customer/configuration read or an authorization token. It is captured inside
// the Job snapshot transaction and replaced by the ordinary delta protocol.
type DocumentCreationInputs struct {
	CanCreate        bool                    `json:"can_create"`
	JobID            int64                   `json:"job_id,omitempty"`
	CustomerID       int64                   `json:"customer_id,omitempty"`
	CompanyToday     string                  `json:"company_today,omitempty"`
	Timezone         string                  `json:"timezone,omitempty"`
	TaxRate          string                  `json:"tax_rate,omitempty"`
	CompanyDefaults  map[string]*string      `json:"company_defaults,omitempty"`
	CustomerDefaults map[string]*string      `json:"customer_defaults,omitempty"`
	Definitions      []DocumentCreationField `json:"definitions,omitempty"`
}

type DocumentCreationField struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Precision      *int     `json:"precision"`
	Required       bool     `json:"required"`
	Retired        bool     `json:"retired"`
	Options        []string `json:"options"`
	RetiredOptions []string `json:"retired_options"`
	Default        *string  `json:"default"`
	HelpText       string   `json:"help_text"`
}

func documentCreationInputs(ctx context.Context, tx pgx.Tx, p Principal, job int64) (map[string]DocumentCreationInputs, error) {
	out := map[string]DocumentCreationInputs{"estimate": {}, "invoice": {}}
	assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, true)
	if err != nil || !assigned {
		return out, err
	}
	if !p.Allows("pricing.view", All) && !p.Allows("pricing.view", Assigned) {
		return out, nil
	}
	if !p.Allows("customer.view", All) && !p.Allows("customer.view", Context) {
		return out, nil
	}
	if !p.Allows("estimate.create", All) && !p.Allows("estimate.create", Assigned) && !p.Allows("invoice.create", All) && !p.Allows("invoice.create", Assigned) {
		return out, nil
	}
	var customer int64
	var today, zone, tax string
	var defaults []byte
	err = tx.QueryRow(ctx, `SELECT c.id, to_char(CURRENT_TIMESTAMP AT TIME ZONE coalesce(nullif(cs.timezone,''),'UTC'),'YYYY-MM-DD'), coalesce(nullif(cs.timezone,''),'UTC'), CASE WHEN c.tax_exempt THEN '0' ELSE cs.default_tax_rate END,
	jsonb_build_object('invoice_notes',cs.default_invoice_notes,'invoice_terms',cs.invoice_payment_terms,'invoice_due_days',cs.default_due_days::text,'estimate_notes',cs.default_estimate_notes,'estimate_terms',cs.estimate_terms,'estimate_valid_days',cs.estimate_valid_days::text,'line_description',cs.default_line_description)
	FROM jobs j JOIN customers c ON c.id=j.customer_id JOIN company_settings cs ON cs.company_id=j.company_id WHERE j.id=$1 AND j.company_id=$2 AND j.deleted_at IS NULL AND c.deleted_at IS NULL`, job, p.CompanyID).Scan(&customer, &today, &zone, &tax, &defaults)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var customerRaw []byte
	err = tx.QueryRow(ctx, `SELECT defaults FROM customer_document_defaults WHERE customer_id=$1`, customer).Scan(&customerRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		customerRaw = []byte(`{}`)
	} else if err != nil {
		return nil, err
	}
	for _, kind := range []string{"estimate", "invoice"} {
		if !p.Allows(kind+".create", All) && !p.Allows(kind+".create", Assigned) {
			continue
		}
		companyDefaults, err := projectDocumentDefaults(defaults, kind)
		if err != nil {
			return nil, err
		}
		customerDefaults, err := projectDocumentDefaults(customerRaw, kind)
		if err != nil {
			return nil, err
		}
		input := DocumentCreationInputs{CanCreate: true, JobID: job, CustomerID: customer, CompanyToday: today, Timezone: zone, TaxRate: tax, CompanyDefaults: companyDefaults, CustomerDefaults: customerDefaults, Definitions: []DocumentCreationField{}}
		rows, err := tx.Query(ctx, `SELECT id,name,field_type,precision,required,retired,options::jsonb,retired_options,default_value,help_text FROM custom_field_definitions WHERE object_type=$1 AND custom_field_applies_to_company(company_id,$2) ORDER BY sort_order,id`, kind, p.CompanyID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var field DocumentCreationField
			if err := rows.Scan(&field.ID, &field.Name, &field.Type, &field.Precision, &field.Required, &field.Retired, &field.Options, &field.RetiredOptions, &field.Default, &field.HelpText); err != nil {
				rows.Close()
				return nil, err
			}
			input.Definitions = append(input.Definitions, field)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out[kind] = input
	}
	return out, nil
}

// MarshalJSON retains empty authorized definitions/defaults while a denied kind
// exposes only its capability, not the inputs of a newly unauthorized action.
func (in DocumentCreationInputs) MarshalJSON() ([]byte, error) {
	if !in.CanCreate {
		return []byte(`{"can_create":false}`), nil
	}
	type wire DocumentCreationInputs
	return json.Marshal(struct {
		wire
		Definitions      []DocumentCreationField `json:"definitions"`
		CustomerDefaults map[string]*string      `json:"customer_defaults"`
	}{wire(in), in.Definitions, in.CustomerDefaults})
}
