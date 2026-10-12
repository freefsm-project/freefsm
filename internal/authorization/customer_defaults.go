package authorization

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/jackc/pgx/v5"
)

type CustomerDefaultsView struct {
	CustomerID, Revision int64
	Manage               bool
	Fields               []ConfigurationField
	Values               map[string]*string
}

func customerDefaultFields() []ConfigurationField {
	return []ConfigurationField{{"invoice_notes", "Invoice notes", "text"}, {"invoice_terms", "Invoice terms", "text"}, {"estimate_notes", "Estimate notes", "text"}, {"estimate_terms", "Estimate terms", "text"}, {"line_description", "Line description", "text"}, {"invoice_due_days", "Invoice due days (blank for no due date)", "number"}, {"estimate_valid_days", "Estimate valid days (blank for none)", "number"}}
}

func (s *Service) CustomerDefaults(ctx context.Context, a Actor, id int64) (CustomerDefaultsView, error) {
	v := CustomerDefaultsView{CustomerID: id, Fields: customerDefaultFields(), Values: map[string]*string{}}
	if err := s.CustomerAccess(ctx, a, id, "customer.financial.view"); err != nil {
		return v, err
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return v, err
	}
	v.Manage = p.Allows("customer.edit", All)
	var data []byte
	err = s.Pool.QueryRow(ctx, `SELECT revision,defaults FROM customer_document_defaults WHERE customer_id=$1`, id).Scan(&v.Revision, &data)
	if err == pgx.ErrNoRows {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(data, &v.Values)
	return v, err
}

func (s *Service) SaveCustomerDefaults(ctx context.Context, a Actor, id, revision int64, form url.Values) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("customer.edit", All) || !p.Allows("customer.financial.view", All) {
			return ErrForbidden
		}
		allowed := map[string]bool{"csrf_token": true, "revision": true}
		for _, field := range customerDefaultFields() {
			allowed[field.Name] = true
			allowed[field.Name+"_inherit"] = true
		}
		for key, submitted := range form {
			if !allowed[key] || len(submitted) != 1 {
				return ErrConfigurationInput
			}
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&active); err != nil {
			return ErrNotFound
		}
		if !active {
			return ErrForbidden
		}
		current := int64(0)
		var data []byte
		err := tx.QueryRow(ctx, `SELECT revision,defaults FROM customer_document_defaults WHERE customer_id=$1`, id).Scan(&current, &data)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if current != revision {
			return ErrConflict
		}
		values := map[string]*string{}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &values); err != nil {
				return err
			}
		}
		for _, f := range customerDefaultFields() {
			if form.Has(f.Name+"_inherit") && form.Get(f.Name+"_inherit") != "on" {
				return ErrConfigurationInput
			}
			if form.Get(f.Name+"_inherit") == "on" {
				values[f.Name] = nil
				continue
			}
			if !form.Has(f.Name) {
				continue
			}
			value := form.Get(f.Name)
			if f.Kind == "number" && value != "" {
				if _, err := validateConfigurationField("default_due_days", value); err != nil {
					return ErrConfigurationInput
				}
			}
			values[f.Name] = &value
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,revision,defaults) VALUES($1,1,$2) ON CONFLICT(customer_id) DO UPDATE SET revision=customer_document_defaults.revision+1,defaults=excluded.defaults`, id, encoded); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "customer.edit", All, "customer", id, map[string]any{"document_defaults": values})
	})
}

// DocumentDefaultInputs is a purpose-limited creation input. Assigned-Job
// authority does not reveal a Customer master or defaults for other document types.
func (s *Service) DocumentDefaultInputs(ctx context.Context, a Actor, kind string, customerID, jobID int64) (map[string]*string, error) {
	if kind != "invoice" && kind != "estimate" {
		return nil, ErrForbidden
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if !p.Allows(kind+".create", All) && !(jobID > 0 && p.Allows(kind+".create", Assigned)) {
		return nil, ErrForbidden
	}
	if _, err := s.DocumentCustomer(ctx, a, customerID, jobID, "0"); err != nil {
		return nil, err
	}
	var data []byte
	err = s.Pool.QueryRow(ctx, `SELECT defaults FROM customer_document_defaults WHERE customer_id=$1`, customerID).Scan(&data)
	if err == pgx.ErrNoRows {
		return map[string]*string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return projectDocumentDefaults(data, kind)
}

func projectDocumentDefaults(data []byte, kind string) (map[string]*string, error) {
	var all map[string]*string
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	values := map[string]*string{}
	for _, key := range []string{kind + "_notes", kind + "_terms", "line_description", kind + "_due_days", kind + "_valid_days"} {
		if value, ok := all[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// Batch the already listable active Customer inputs for an All-Records creation
// form. Job-bound inputs use DocumentDefaultInputs for the exact selected Job.
func (s *Service) DocumentDefaultChoices(ctx context.Context, a Actor, kind string, ids []int64) (map[int64]map[string]*string, error) {
	out := map[int64]map[string]*string{}
	if kind != "invoice" && kind != "estimate" {
		return nil, ErrForbidden
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || !p.Allows(kind+".create", All) || !p.Allows("customer.view", All) || !p.Allows("pricing.view", All) {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.id,coalesce(d.defaults,'{}'::jsonb) FROM customers c LEFT JOIN customer_document_defaults d ON d.customer_id=c.id WHERE c.company_id=$1 AND c.deleted_at IS NULL AND c.id=ANY($2)`, p.CompanyID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		values, err := projectDocumentDefaults(data, kind)
		if err != nil {
			return nil, err
		}
		out[id] = values
	}
	return out, rows.Err()
}
