package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
)

// RunLeaseBilling joins normal startup, maintenance drain and restored-pool
// refresh. A pass owns admission until all its transactions have completed.
func (s *Service) RunLeaseBilling(ctx context.Context, control *instancecontrol.Control, refresh func() error) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		work, release, err := control.Enter(ctx)
		if err == nil {
			err = refresh()
			if err == nil {
				bounded, cancel := context.WithTimeout(work, 30*time.Second)
				err = s.GenerateLeaseDrafts(bounded)
				cancel()
			}
			release()
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("Lease billing worker unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
		}
	}
}

// GenerateLeaseDrafts consumes approved agreements, never a product identity or
// submitted Invoice content. Each pass is bounded; oldest attempted agreements
// go first so a failing agreement cannot starve the rest after restart.
func (s *Service) GenerateLeaseDrafts(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT f.lease_id FROM lease_finalizations f LEFT JOIN lease_billing_worker_state w USING(lease_id) ORDER BY w.attempted_at NULLS FIRST,f.lease_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := s.generateLeaseDrafts(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("lease %d: %w", id, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

func (s *Service) generateLeaseDrafts(ctx context.Context, id int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	// A savepoint preserves visible failure without partial commercial effects.
	work, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	generationErr := s.generateLeasePeriods(ctx, work, id)
	if generationErr == nil {
		// Evaluate deferred storage invariants before releasing the savepoint.
		_, generationErr = work.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	}
	if generationErr != nil {
		if err := work.Rollback(ctx); err != nil {
			return errors.Join(generationErr, err)
		}
	} else if err := work.Commit(ctx); err != nil {
		return err
	}
	message := ""
	if generationErr != nil {
		message = "Draft generation failed; review current agreement, coverage and billing terms, then retry."
	}
	if _, err := tx.Exec(ctx, `INSERT INTO lease_billing_worker_state(lease_id,failure) VALUES($1,$2) ON CONFLICT(lease_id) DO UPDATE SET attempted_at=clock_timestamp(),failure=excluded.failure`, id, message); err != nil {
		return errors.Join(generationErr, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(generationErr, err)
	}
	return generationErr
}

func (s *Service) generateLeasePeriods(ctx context.Context, tx pgx.Tx, id int64) error {
	var company int64
	var start, end, cutoff, anchor, today time.Time
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT l.company_id,f.starts_on,f.fixed_ends_on,f.managed_billing_start,f.monthly_anchor,f.terms,lease_dependency_today(l.company_id) FROM lease_finalizations f JOIN leases l ON l.id=f.lease_id WHERE f.lease_id=$1 FOR UPDATE OF l`, id).Scan(&company, &start, &end, &cutoff, &anchor, &raw, &today); err != nil {
		return err
	}
	p := Principal{CompanyID: company}
	base, err := readLeaseBase(ctx, tx, p, id)
	if err != nil {
		return err
	}
	if base.Archived || base.Lifecycle == "draft" {
		return ErrConflict
	}
	var eligible, exempt bool
	if err := tx.QueryRow(ctx, `SELECT NOT archived AND customer_id=$2 AND premises_id IS NOT DISTINCT FROM $3 AND lifecycle<>'draft' FROM lease_master_dependencies WHERE lease_id=$1 FOR SHARE`, id, base.CustomerID, base.PremisesID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible || base.StartsOn != start.Format(time.DateOnly) || base.EndsOn != end.Format(time.DateOnly) {
		return ErrConflict
	}
	if base.Lifecycle == "terminated" {
		for _, coverage := range base.Coverage {
			if coverage.EndsOn == nil {
				return &LeaseValidationError{Message: "Terminated coverage requires a known inclusive end before billing"}
			}
		}
	}
	if err := tx.QueryRow(ctx, `SELECT c.tax_exempt,c.deleted_at IS NULL AND l.deleted_at IS NULL AND l.company_id=$2 AND l.object_type='customer' AND l.object_id=c.id FROM customers c JOIN locations l ON l.id=$3 WHERE c.id=$1 AND c.company_id=$2 FOR SHARE OF c,l`, base.CustomerID, company, base.PremisesID).Scan(&exempt, &eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	var terms []DraftLeaseTerms
	if err := json.Unmarshal(raw, &terms); err != nil {
		return err
	}
	// Existing pending rows always precede newly materialized months. A generation
	// remains consumed even if its ordinary Invoice was subsequently voided.
	rows, err := tx.Query(ctx, `SELECT o.kind,o.due_on FROM lease_billing_obligations o WHERE lease_id=$1 AND due_on<= $2::date+30 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id) ORDER BY due_on,kind LIMIT 24`, id, today)
	if err != nil {
		return err
	}
	type period struct {
		kind string
		due  time.Time
	}
	var periods []period
	for rows.Next() {
		var next period
		if err := rows.Scan(&next.kind, &next.due); err != nil {
			rows.Close()
			return err
		}
		periods = append(periods, next)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var next time.Time
	if err := tx.QueryRow(ctx, `SELECT coalesce((max(due_on)+interval '1 month')::date,$2::date) FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, id, anchor).Scan(&next); err != nil {
		return err
	}
	if next.Before(cutoff) {
		next = time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, time.UTC)
		if next.Before(cutoff) {
			next = next.AddDate(0, 1, 0)
		}
	}
	for len(periods) < 24 && !next.After(today.AddDate(0, 0, 30)) {
		periods = append(periods, period{"monthly", next})
		next = next.AddDate(0, 1, 0)
	}
	for _, period := range periods {
		if period.due.Before(cutoff) {
			return ErrConflict
		}
		coveredAt := period.due
		if period.kind == "initial" {
			coveredAt = start
		}
		selected := append([]DraftLeaseTerms(nil), terms...)
		for i := range selected {
			approved := selected[i].Selected
			selected[i].Selected = false
			if !approved {
				continue
			}
			for _, coverage := range base.Coverage {
				day := coveredAt.Format(time.DateOnly)
				if coverage.AssetID == selected[i].AssetID && coverage.StartsOn <= day && (coverage.EndsOn == nil || *coverage.EndsOn >= day) {
					selected[i].Selected = true
				}
			}
			if !selected[i].Selected {
				continue
			}
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND company_id=$2 AND ownership_class='company_owned' FROM assets WHERE id=$1 FOR SHARE`, selected[i].AssetID, company).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return ErrConflict
			}
			if _, err := tx.Exec(ctx, `SELECT lease_asset_timeline_validate($1)`, selected[i].AssetID); err != nil {
				return err
			}
		}
		for _, coverage := range base.Coverage {
			day := coveredAt.Format(time.DateOnly)
			if coverage.StartsOn > day || (coverage.EndsOn != nil && *coverage.EndsOn < day) {
				continue
			}
			approved := false
			for _, term := range selected {
				if term.Selected && term.AssetID == coverage.AssetID {
					approved = true
				}
			}
			if !approved {
				return &LeaseValidationError{Message: "Covered Asset requires approved billing terms"}
			}
		}
		input, err := leaseObligationInput(selected, period.kind == "monthly", exempt)
		if err != nil {
			return err
		}
		obligation, financial, lines, err := materializeLeaseObligation(ctx, tx, base, selected, period.kind, period.due, input)
		if err != nil {
			return err
		}
		invoice, err := s.createApprovedDocument(ctx, tx, Actor{}, p, "invoice", DocumentCreation{CustomerID: base.CustomerID, Title: base.Number + " " + period.kind + " " + period.due.Format(time.DateOnly), TaxRate: input.TaxRate, LineItems: lines, Financial: financial, CustomFields: "[]", InvoiceDate: today, DueDate: period.due}, func(string) (string, error) { return "unpaid", nil }, obligation, &leaseGenerationCapability{obligation: obligation, company: company})
		if err != nil {
			return err
		}
		if err := recordLeaseGeneration(ctx, tx, id, obligation, invoice); err != nil {
			return err
		}
	}
	return nil
}
