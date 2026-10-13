package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/saveaction"
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
		if typ == "estimate" || typ == "invoice" {
			if err := saveaction.RequireRevision(ctx, tx, p.UserID, fmt.Sprintf("/%ss/%d", typ, id)); err != nil {
				return err
			}
		}
		fields, err = customfields.Validate(ctx, tx, typ, old, &input, mode, p.CompanyID)
		if err != nil {
			return err
		}
		table, _ := customfields.ParentTable(typ)
		if err = customfields.CheckCycleValues(ctx, tx, typ, id, fields); err != nil {
			return err
		}
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
		return s.detailsInTransaction(ctx, tx, a, p, typ, id, jobID, input, write)
	})
}

// DetailsInTransaction joins an already fenced command (for example a durable
// API operation). Validation, effect, audit and receipt commit together.
func (s *Service) DetailsInTransaction(ctx context.Context, tx pgx.Tx, a Actor, typ string, id, jobID int64, input *string, write func(pgx.Tx, string) (int64, error)) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	return s.detailsInTransaction(ctx, tx, a, p, typ, id, jobID, input, write)
}

func (s *Service) detailsInTransaction(ctx context.Context, tx pgx.Tx, a Actor, p Principal, typ string, id, jobID int64, input *string, write func(pgx.Tx, string) (int64, error)) error {
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
		if typ == "job" || ((typ == "estimate" || typ == "invoice") && !commercialReviewed(ctx)) {
			if err := saveaction.RequireRevision(ctx, tx, p.UserID, fmt.Sprintf("/%ss/%d", typ, id)); err != nil {
				return err
			}
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
	} else if commercialReviewed(ctx) {
		mode = customfields.ChangedValues
	}
	fields, err := customfields.Validate(ctx, tx, typ, old, input, mode, p.CompanyID)
	if err != nil {
		return err
	}
	if err = customfields.CheckCycleValues(ctx, tx, typ, id, fields); err != nil {
		return err
	}
	var parentBefore, parentAfter json.RawMessage
	snapshot := `to_jsonb(a)`
	if typ == "job" {
		snapshot += ` || jsonb_build_object('contact_ids',(SELECT coalesce(jsonb_agg(jc.contact_id ORDER BY jc.contact_id),'[]'::jsonb) FROM job_contacts jc WHERE jc.job_id=a.id))`
	}
	if (typ == "asset" || typ == "job" || typ == "estimate" || typ == "invoice") && id > 0 {
		if err := tx.QueryRow(ctx, `SELECT `+snapshot+` FROM `+typ+`s a WHERE id=$1`, id).Scan(&parentBefore); err != nil {
			return err
		}
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
	if typ == "asset" || typ == "job" || typ == "estimate" || typ == "invoice" {
		if err := tx.QueryRow(ctx, `SELECT `+snapshot+` FROM `+typ+`s a WHERE id=$1`, result).Scan(&parentAfter); err != nil {
			return err
		}
		if (typ == "estimate" || typ == "invoice") && id > 0 {
			if err := commercialDetailsCheck(ctx, tx, p, typ, parentBefore, parentAfter); err != nil {
				return err
			}
			parentBefore, parentAfter, err = commercialChanges(parentBefore, parentAfter)
			if err != nil {
				return err
			}
		}
		if typ == "job" {
			var before, after map[string]json.RawMessage
			if len(parentBefore) > 0 {
				if err := json.Unmarshal(parentBefore, &before); err != nil {
					return err
				}
			}
			if err := json.Unmarshal(parentAfter, &after); err != nil {
				return err
			}
			changed := false
			for _, key := range []string{"project_id", "location_id", "customer_contact_id", "asset_id"} {
				if !bytes.Equal(before[key], after[key]) && string(after[key]) != "null" {
					changed = true
				}
				if id > 0 && !bytes.Equal(before[key], after[key]) {
					changed = true
				}
			}
			if id > 0 && !bytes.Equal(before["customer_id"], after["customer_id"]) {
				// Automatic clearing is audited as part of planning's Customer
				// replacement; do not invent use of a relationship grant.
				changed = false
				if err := auditJobCommercialReassignment(ctx, tx, a, p, result, before["customer_id"], after["customer_id"]); err != nil {
					return err
				}
			}
			if changed {
				if err := audit(ctx, tx, a, p, "job.relations.edit", All, typ, result, map[string]any{"before": parentBefore, "after": parentAfter}); err != nil {
					return err
				}
			}
		}
		return audit(ctx, tx, a, p, permission, scope, typ, result, map[string]any{"before": parentBefore, "after": parentAfter, "custom_fields_before": old, "custom_fields_after": fields})
	}
	return audit(ctx, tx, a, p, permission, scope, typ, result, map[string]any{"custom_fields_before": old, "custom_fields_after": fields})
}
