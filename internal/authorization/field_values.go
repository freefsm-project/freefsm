package authorization

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/jackc/pgx/v5"
)

// fieldParent locks the parent before checking lifecycle, current assignment and
// settlement. Callers already hold the shared commit-time authorization fence.
func fieldParent(ctx context.Context, tx pgx.Tx, p Principal, typ string, id int64, permission string, mutation ...bool) (string, Scope, error) {
	scope, err := parentAccess(ctx, tx, p, typ, id, permission, true)
	if err != nil {
		return "", "", err
	}
	table, _ := customfields.ParentTable(typ)
	var fields string
	if err = tx.QueryRow(ctx, `SELECT custom_fields::text FROM `+table+` WHERE id=$1`, id).Scan(&fields); err != nil {
		return "", "", err
	}
	if typ == "invoice" && len(mutation) > 0 && mutation[0] {
		var settled bool
		if err := tx.QueryRow(ctx, `SELECT invoice_has_active_settlement($1)`, id).Scan(&settled); err != nil {
			return "", "", err
		}
		if settled {
			return "", "", &customfields.FieldError{Name: "Invoice Custom Fields", Message: "commercial fields are locked by active settlement"}
		}
	}
	return fields, scope, nil
}

func (s *Service) DetailsAccess(ctx context.Context, a Actor, typ string, id int64) error {
	if id == 0 {
		p, err := s.Principal(ctx, a)
		if err != nil {
			return err
		}
		if !p.Allows(typ+".create", All) {
			return ErrForbidden
		}
		return nil
	}
	return s.ParentAccess(ctx, a, typ, id, typ+".edit")
}

func (s *Service) ReadFields(ctx context.Context, a Actor, typ string, id int64, edit bool) (string, error) {
	var fields string
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := ""
		if edit {
			permission = typ + ".edit"
			if typ == "job" {
				permission = "job.fields.edit"
			}
		}
		var err error
		fields, _, err = fieldParent(ctx, tx, p, typ, id, permission)
		return err
	})
	return fields, err
}

func (s *Service) SaveFields(ctx context.Context, a Actor, typ string, id int64, input string) (string, error) {
	return s.saveFields(ctx, a, typ, id, input, customfields.Details)
}

// PatchFields is for atomic changed-value reconciliation. Missing unrelated
// required values defer to a subsequent online details save; no defaults apply.
func (s *Service) PatchFields(ctx context.Context, a Actor, typ string, id int64, input string) (string, error) {
	return s.saveFields(ctx, a, typ, id, input, customfields.ChangedValues)
}

func (s *Service) saveFields(ctx context.Context, a Actor, typ string, id int64, input string, mode customfields.SaveMode) (string, error) {
	var fields string
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := typ + ".edit"
		if typ == "job" {
			permission = "job.fields.edit"
		}
		old, scope, err := fieldParent(ctx, tx, p, typ, id, permission, true)
		if err != nil {
			return err
		}
		fields, err = customfields.Validate(ctx, tx, typ, old, &input, mode, p.CompanyID)
		if err != nil {
			return err
		}
		table, _ := customfields.ParentTable(typ)
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET custom_fields=$2::jsonb,updated_at=clock_timestamp() WHERE id=$1`, id, fields); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, permission, scope, typ, id, map[string]any{"before": old, "after": fields})
	})
	return fields, err
}

// DetailsTransaction integrates the existing parent domains. Future domains add
// their own permission/lifecycle adapter and reuse customfields.Validate.
// The callback must perform ALL details effects using the supplied tx.
// Separate actions and lifecycle recovery must not call this details boundary.
func (s *Service) DetailsTransaction(ctx context.Context, a Actor, typ string, id, jobID int64, input *string, write func(pgx.Tx, string) (int64, error)) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := typ + ".edit"
		old := "[]"
		scope := All
		if id == 0 {
			permission = typ + ".create"
			if !p.Allows(permission, All) {
				if jobID == 0 || !p.Allows(permission, Assigned) {
					return ErrForbidden
				}
				if _, _, err := fieldParent(ctx, tx, p, "job", jobID, ""); err != nil {
					return err
				}
				var assigned bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$1 AND user_id=$2 FOR SHARE)`, jobID, p.UserID).Scan(&assigned); err != nil {
					return err
				}
				if !assigned {
					return ErrForbidden
				}
				scope = Assigned
			}
		} else {
			var err error
			old, scope, err = fieldParent(ctx, tx, p, typ, id, permission, true)
			if err != nil {
				return err
			}
		}
		var fieldScope Scope
		if typ == "job" && input != nil && *input != "[]" {
			if id == 0 {
				if !p.Allows("job.fields.edit", All) {
					return ErrForbidden
				}
				fieldScope = All
			} else {
				var err error
				_, fieldScope, err = fieldParent(ctx, tx, p, "job", id, "job.fields.edit", true)
				if err != nil {
					return err
				}
			}
		}
		mode := customfields.Details
		if id == 0 {
			mode = customfields.Creation
		}
		fields, err := customfields.Validate(ctx, tx, typ, old, input, mode, p.CompanyID)
		if err != nil {
			return err
		}
		result, err := write(tx, fields)
		if err != nil {
			return err
		}
		if result <= 0 {
			return fmt.Errorf("details save did not return a parent identity")
		}
		// Details may themselves alter a Job binding/assignment. Locks prevent
		// concurrent scope loss, but cannot prevent this transaction changing its
		// own scope, so recheck both independently authorized effects after writes.
		if _, err := parentAccess(ctx, tx, p, typ, result, permission, true); err != nil {
			return err
		}
		if fieldScope != "" {
			if _, err := parentAccess(ctx, tx, p, "job", result, "job.fields.edit", true); err != nil {
				return err
			}
		}
		if fieldScope != "" && fields != old {
			if err := audit(ctx, tx, a, p, "job.fields.edit", fieldScope, typ, result, map[string]any{"before": old, "after": fields}); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, permission, scope, typ, result, map[string]any{"custom_fields_before": old, "custom_fields_after": fields})
	})
}
