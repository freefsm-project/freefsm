package authorization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type ConversionAuthority struct {
	principal                 Principal
	actor                     Actor
	convertScope, createScope Scope
	estimateID                int64
}

type ReversionAuthority struct {
	actor     Actor
	principal Principal
	scope     Scope
	invoiceID int64
}

// A reviewed command may report cycle drift for its exact former target, after
// current scoped authority is rechecked. This grants no tombstone content read
// and cannot authorize another cycle or produce a reversion approval.
func (s *Service) AuthorizeReviewedReversion(ctx context.Context, tx pgx.Tx, a Actor, id int64) (ReversionAuthority, error) {
	approval, err := s.AuthorizeReversion(ctx, tx, a, id)
	if !errors.Is(err, ErrNotFound) {
		return approval, err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return ReversionAuthority{}, err
	}
	var job *int64
	err = tx.QueryRow(ctx, `SELECT job_id FROM invoices i WHERE id=$1 AND company_id=$2 AND conversion_hidden_at IS NOT NULL AND EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c WHERE c.invoice_id=i.id AND c.reverted_at IS NOT NULL)`, id, p.CompanyID).Scan(&job)
	if err == pgx.ErrNoRows {
		return ReversionAuthority{}, ErrNotFound
	}
	if err != nil {
		return ReversionAuthority{}, err
	}
	if p.Allows("invoice.revert", All) {
		return ReversionAuthority{}, ErrConversionCycle
	}
	if job != nil && p.Allows("invoice.revert", Assigned) {
		assigned, err := assignedJobAccess(ctx, tx, p.UserID, *job, true)
		if err != nil {
			return ReversionAuthority{}, err
		}
		if assigned {
			return ReversionAuthority{}, ErrConversionCycle
		}
	}
	return ReversionAuthority{}, ErrNotFound
}

func (s *Service) AuthorizeReversion(ctx context.Context, tx pgx.Tx, a Actor, id int64) (ReversionAuthority, error) {
	var out ReversionAuthority
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return out, err
	}
	for _, query := range []string{`SELECT id FROM users WHERE id=$1 FOR SHARE`, `SELECT r.id FROM authorization_roles r JOIN users u ON u.authorization_role_id=r.id WHERE u.id=$1 FOR SHARE OF r`, `SELECT role_id FROM authorization_grants WHERE role_id=(SELECT authorization_role_id FROM users WHERE id=$1) FOR SHARE`} {
		if _, err := tx.Exec(ctx, query, a.UserID); err != nil {
			return out, err
		}
	}
	if a.SessionHash != "" {
		if _, err := tx.Exec(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND token_hash=$2 FOR SHARE`, a.UserID, a.SessionHash); err != nil {
			return out, err
		}
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return out, err
	}
	scope, err := parentAccess(ctx, tx, p, "invoice", id, "invoice.revert", true)
	if err != nil {
		return out, err
	}
	return ReversionAuthority{a, p, scope, id}, nil
}

func (s *Service) AuditRevertedDocument(ctx context.Context, tx pgx.Tx, approval ReversionAuthority, estimate int64) error {
	p, err := principal(ctx, tx, approval.actor, false)
	if err != nil {
		return err
	}
	if p.RoleID != approval.principal.RoleID || !p.Allows("invoice.revert", approval.scope) {
		return ErrForbidden
	}
	return audit(ctx, tx, approval.actor, p, "invoice.revert", approval.scope, "invoice", approval.invoiceID, map[string]any{"estimate_id": estimate})
}

// AuthorizeConversion joins the existing conversion transaction without taking
// ownership of its cycles, relation transfers or idempotency. Locking authority
// rows also forces a Serializable retry if authority changed while waiting.
func (s *Service) AuthorizeConversion(ctx context.Context, tx pgx.Tx, a Actor, id int64) (ConversionAuthority, error) {
	var out ConversionAuthority
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return out, err
	}
	for _, query := range []string{`SELECT id FROM users WHERE id=$1 FOR SHARE`, `SELECT r.id FROM authorization_roles r JOIN users u ON u.authorization_role_id=r.id WHERE u.id=$1 FOR SHARE OF r`} {
		if _, err := tx.Exec(ctx, query, a.UserID); err != nil {
			return out, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT role_id FROM authorization_grants WHERE role_id=(SELECT authorization_role_id FROM users WHERE id=$1) FOR SHARE`, a.UserID); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT role_id FROM authorization_grants WHERE role_id=(SELECT authorization_role_id FROM users WHERE id=$1) FOR SHARE`, a.UserID); err != nil {
		return out, err
	}
	if a.SessionHash != "" {
		if _, err := tx.Exec(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND token_hash=$2 FOR SHARE`, a.UserID, a.SessionHash); err != nil {
			return out, err
		}
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return out, err
	}
	var customer int64
	var job *int64
	var active bool
	if err := tx.QueryRow(ctx, `SELECT customer_id,job_id,deleted_at IS NULL FROM estimates WHERE id=$1 AND company_id=$2 FOR SHARE`, id, p.CompanyID).Scan(&customer, &job, &active); err == pgx.ErrNoRows {
		return out, ErrNotFound
	} else if err != nil {
		return out, err
	}
	if !active {
		return out, ErrForbidden
	}
	if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 AND company_id=$2 FOR SHARE`, customer, p.CompanyID).Scan(&active); err != nil {
		return out, ErrNotFound
	}
	if !active {
		return out, ErrForbidden
	}
	assigned := false
	if job != nil {
		var owner int64
		if err := tx.QueryRow(ctx, `SELECT customer_id,deleted_at IS NULL FROM jobs WHERE id=$1 AND company_id=$2 FOR SHARE`, *job, p.CompanyID).Scan(&owner, &active); err != nil {
			return out, ErrNotFound
		}
		if owner != customer {
			return out, ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT user_id FROM job_assignments WHERE job_id=$1 AND user_id=$2 FOR SHARE`, *job, p.UserID)
		if err != nil {
			return out, err
		}
		// Conversion may carry this already-linked archived Job for All Records
		// authority. It does not reactivate Assigned Jobs authority or permit a
		// new archived Job selection through ordinary document creation.
		assigned = rows.Next() && active
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	scopes := []Scope{}
	for _, permission := range []string{"estimate.convert", "invoice.create"} {
		if p.Allows(permission, All) {
			scopes = append(scopes, All)
		} else if assigned && p.Allows(permission, Assigned) {
			scopes = append(scopes, Assigned)
		} else {
			return out, ErrForbidden
		}
	}
	return ConversionAuthority{p, a, scopes[0], scopes[1], id}, nil
}

func (s *Service) AuditConvertedDocument(ctx context.Context, tx pgx.Tx, approval ConversionAuthority, invoice int64) error {
	p, err := principal(ctx, tx, approval.actor, false)
	if err != nil {
		return err
	}
	if p.RoleID != approval.principal.RoleID || !p.Allows("estimate.convert", approval.convertScope) || !p.Allows("invoice.create", approval.createScope) {
		return ErrForbidden
	}
	if err := audit(ctx, tx, approval.actor, p, "estimate.convert", approval.convertScope, "estimate", approval.estimateID, map[string]any{"invoice_id": invoice}); err != nil {
		return err
	}
	return audit(ctx, tx, approval.actor, p, "invoice.create", approval.createScope, "invoice", invoice, map[string]any{"source_estimate_id": approval.estimateID})
}

// Supplying new Invoice values is an independently authorized edit, rather than
// an inseparable effect of copying the Estimate.
func (s *Service) AuthorizeConversionValues(ctx context.Context, tx pgx.Tx, approval ConversionAuthority) error {
	p, err := principal(ctx, tx, approval.actor, false)
	if err != nil {
		return err
	}
	if p.Allows("invoice.edit", All) {
		return nil
	}
	if !p.Allows("invoice.edit", Assigned) {
		return ErrForbidden
	}
	var assigned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM estimates e JOIN jobs j ON j.id=e.job_id JOIN job_assignments a ON a.job_id=j.id WHERE e.id=$1 AND j.deleted_at IS NULL AND a.user_id=$2)`, approval.estimateID, p.UserID).Scan(&assigned); err != nil {
		return err
	}
	if !assigned {
		return ErrForbidden
	}
	return nil
}

func (s *Service) AuditConversionValues(ctx context.Context, tx pgx.Tx, approval ConversionAuthority, invoice int64) error {
	if err := s.AuthorizeConversionValues(ctx, tx, approval); err != nil {
		return err
	}
	p, err := principal(ctx, tx, approval.actor, false)
	if err != nil {
		return err
	}
	scope := All
	if !p.Allows("invoice.edit", All) {
		scope = Assigned
	}
	return audit(ctx, tx, approval.actor, p, "invoice.edit", scope, "invoice", invoice, map[string]any{"source_estimate_id": approval.estimateID, "conversion_values": true})
}
