package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CustomerContext is deliberately closed. Never embed a master entity here.
type CustomerContext struct {
	ID               int64  `json:"id"`
	DisplayName      string `json:"display_name"`
	FirstName        string `json:"first_name"`
	LastName         string `json:"last_name"`
	OrganizationName string `json:"organization_name"`
	Kind             string `json:"kind"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	ServiceNotes     string `json:"service_notes"`
}

func (s *Service) JobCustomer(ctx context.Context, a Actor, jobID int64) (CustomerContext, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return CustomerContext{}, err
	}
	if !p.Allows("customer.view", Context) || !p.Allows("job.view", Assigned) {
		return CustomerContext{}, ErrNotFound
	}
	var c CustomerContext
	// All Records does not turn this Job-context route into an unrestricted
	// master route: explicit current assignment and active Job remain required.
	err = s.Pool.QueryRow(ctx, `SELECT c.id,c.display_name,c.first_name,c.last_name,c.company_name,
 CASE c.account_type WHEN 'company' THEN 'Organization' WHEN 'individual' THEN 'Individual' ELSE c.account_type END,c.phone,c.email,c.service_notes
 FROM jobs j JOIN job_assignments a ON a.job_id=j.id JOIN customers c ON c.id=j.customer_id
 JOIN users u ON u.id=a.user_id JOIN authorization_grants g ON g.role_id=u.authorization_role_id
 WHERE j.id=$1 AND a.user_id=$2 AND u.is_active AND g.permission='customer.view' AND g.scope IN ('A','C')
 AND EXISTS(SELECT 1 FROM authorization_grants jg WHERE jg.role_id=u.authorization_role_id AND jg.permission='job.view' AND jg.scope IN ('J','A'))
 AND j.deleted_at IS NULL AND c.deleted_at IS NULL AND c.company_id=$3 AND j.company_id=$3`, jobID, a.UserID, p.CompanyID).Scan(&c.ID, &c.DisplayName, &c.FirstName, &c.LastName, &c.OrganizationName, &c.Kind, &c.Phone, &c.Email, &c.ServiceNotes)
	if err == pgx.ErrNoRows {
		return c, ErrNotFound
	}
	return c, err
}

// CustomerInput is a patch: omitted fields preserve recorded values. Billing
// and tax changes require independently granted financial facet View.
type CustomerInput struct {
	FirstName       *string `json:"first_name,omitempty"`
	LastName        *string `json:"last_name,omitempty"`
	DisplayName     *string `json:"display_name,omitempty"`
	Email           *string `json:"email,omitempty"`
	Phone           *string `json:"phone,omitempty"`
	CompanyName     *string `json:"company_name,omitempty"`
	Notes           *string `json:"notes,omitempty"`
	Status          *string `json:"status,omitempty"`
	ServiceNotes    *string `json:"service_notes,omitempty"`
	AccountType     *string `json:"account_type,omitempty"`
	BillingAddress1 *string `json:"billing_address_1,omitempty"`
	BillingAddress2 *string `json:"billing_address_2,omitempty"`
	BillingCity     *string `json:"billing_city,omitempty"`
	BillingState    *string `json:"billing_state,omitempty"`
	BillingZipCode  *string `json:"billing_zip_code,omitempty"`
	TaxExempt       *bool   `json:"tax_exempt,omitempty"`
	CustomFields    *string `json:"custom_fields,omitempty"`
}

func (s *Service) SaveCustomer(ctx context.Context, a Actor, id int64, in CustomerInput, actions ...WebAction) (*ent.Customer, error) {
	var result *ent.Customer
	var action WebAction
	if len(actions) > 0 {
		action = actions[0]
		if action.ID == "" {
			return nil, ErrConflict
		}
	}
	intent := customerIntent(id, in, action)
	if action.ID != "" {
		if _, err := uuid.Parse(action.ID); err != nil {
			return nil, ErrConflict
		}
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if action.ID != "" {
			var savedIntent string
			var effect int64
			err := tx.QueryRow(ctx, `SELECT intent_hash,effect_id FROM web_action_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, action.ID).Scan(&savedIntent, &effect)
			if err == nil {
				if savedIntent != intent {
					return ErrConflict
				}
				result = &ent.Customer{ID: effect}
				return nil
			}
			if err != pgx.ErrNoRows {
				return err
			}
		}
		permission := "customer.edit"
		if id == 0 {
			permission = "customer.create"
		}
		if !p.Allows("customer.view", All) {
			if id != 0 {
				return ErrNotFound
			}
			return ErrForbidden
		}
		if id != 0 {
			var active bool
			var updated time.Time
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL,updated_at FROM customers WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&active, &updated); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			} else if !active {
				return ErrForbidden
			}
			if action.ID != "" && action.Revision != CustomerRevision(&ent.Customer{ID: id, UpdatedAt: updated}) {
				return ErrConflict
			}
		}
		if !p.Allows(permission, All) {
			return ErrForbidden
		}
		if id == 0 && (in.DisplayName == nil || strings.TrimSpace(*in.DisplayName) == "") {
			name := ""
			if in.FirstName != nil {
				name = *in.FirstName
			}
			if in.LastName != nil {
				name += " " + *in.LastName
			}
			name = strings.TrimSpace(name)
			if name == "" && in.CompanyName != nil {
				name = strings.TrimSpace(*in.CompanyName)
			}
			in.DisplayName = &name
		}
		if in.DisplayName != nil && strings.TrimSpace(*in.DisplayName) == "" {
			return &CustomerValidationError{Field: "display_name", Message: "Enter a display name, person name or company name."}
		}
		if in.AccountType != nil && *in.AccountType != "individual" && *in.AccountType != "company" {
			return &CustomerValidationError{Field: "account_type", Message: "Choose Individual or Company."}
		}
		oldFields := "[]"
		if id != 0 {
			if err := tx.QueryRow(ctx, `SELECT custom_fields::text FROM customers WHERE id=$1`, id).Scan(&oldFields); err != nil {
				return err
			}
		}
		mode := customfields.Details
		if id == 0 {
			mode = customfields.Creation
		}
		fields, fieldErr := customfields.Validate(ctx, tx, "customer", oldFields, in.CustomFields, mode, p.CompanyID)
		if fieldErr != nil {
			return fieldErr
		}
		in.CustomFields = &fields
		if !p.Allows("customer.financial.view", All) && (in.BillingAddress1 != nil || in.BillingAddress2 != nil || in.BillingCity != nil || in.BillingState != nil || in.BillingZipCode != nil || in.TaxExempt != nil) {
			return ErrForbidden
		}
		if id == 0 {
			if in.DisplayName == nil {
				return fmt.Errorf("display name is required")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,$2) RETURNING id`, p.CompanyID, *in.DisplayName).Scan(&id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE customers SET first_name=coalesce($2,first_name),last_name=coalesce($3,last_name),display_name=coalesce($4,display_name),email=coalesce($5,email),phone=coalesce($6,phone),company_name=coalesce($7,company_name),notes=coalesce($8,notes),service_notes=coalesce($9,service_notes),account_type=coalesce($10,account_type),billing_address_1=coalesce($11,billing_address_1),billing_address_2=coalesce($12,billing_address_2),billing_city=coalesce($13,billing_city),billing_state=coalesce($14,billing_state),billing_zip_code=coalesce($15,billing_zip_code),tax_exempt=coalesce($16,tax_exempt),custom_fields=coalesce($17::jsonb,custom_fields),status=coalesce($18,status),updated_at=clock_timestamp() WHERE id=$1`, id, in.FirstName, in.LastName, in.DisplayName, in.Email, in.Phone, in.CompanyName, in.Notes, in.ServiceNotes, in.AccountType, in.BillingAddress1, in.BillingAddress2, in.BillingCity, in.BillingState, in.BillingZipCode, in.TaxExempt, in.CustomFields, in.Status)
		if err != nil {
			return err
		}
		var auditDetail any = in
		if action.ID != "" {
			auditDetail = map[string]any{"input": in, "operation_id": action.ID}
		}
		if err := audit(ctx, tx, a, p, permission, All, "customer", id, auditDetail); err != nil {
			return err
		}
		activityAction := "updated"
		if permission == "customer.create" {
			activityAction = "created"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) SELECT $1,$2,$3,'customer',id,jsonb_build_object('entity_name',display_name,'permission',$4::text,'scope','A') FROM customers WHERE id=$5`, p.CompanyID, a.UserID, activityAction, permission, id); err != nil {
			return err
		}
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(c)||jsonb_build_object('custom_fields',c.custom_fields::text) FROM customers c WHERE id=$1`, id).Scan(&encoded); err != nil {
			return err
		}
		result = &ent.Customer{}
		if err := json.Unmarshal(encoded, result); err != nil {
			return err
		}
		if !p.Allows("customer.financial.view", All) {
			redactCustomerFinancial(result)
		}
		if action.ID != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO web_action_receipts(user_id,operation_id,intent_hash,action,effect_id,disposition) VALUES($1,$2,$3,'customer.save',$4,'saved')`, a.UserID, action.ID, intent, id); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func redactCustomerFinancial(c *ent.Customer) {
	c.BillingAddress1 = ""
	c.BillingAddress2 = ""
	c.BillingCity = ""
	c.BillingState = ""
	c.BillingZipCode = ""
	c.TaxExempt = false
}

func (s *Service) CustomerAccess(ctx context.Context, a Actor, id int64, permission string) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	if !p.Allows("customer.view", All) {
		return ErrNotFound
	}
	var active bool
	if err := s.Pool.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&active); err == pgx.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if !active && !p.Allows("customer.archived.view", All) {
		return ErrNotFound
	}
	if !p.Allows(permission, All) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) CustomerLifecycle(ctx context.Context, a Actor, id int64, restore bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := "customer.archive"
		if restore {
			permission = "customer.restore"
		}
		if !p.Allows("customer.view", All) {
			return ErrNotFound
		}
		if !p.Allows(permission, All) {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE customers SET deleted_at=CASE WHEN $3 THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp() WHERE id=$1 AND company_id=$2`, id, p.CompanyID, restore)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit(ctx, tx, a, p, permission, All, "customer", id, nil)
	})
}
