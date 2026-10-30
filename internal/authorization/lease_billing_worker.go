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
	materializationErr := s.materializeLeasePeriods(ctx, id)
	var isolated *leasePeriodFailures
	if materializationErr != nil && !errors.As(materializationErr, &isolated) {
		return materializationErr
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	// The previous transaction committed approved period identities before any
	// Invoice work. A crash here leaves pending obligations, never new retry IDs.
	work, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	generationErr := s.generateLeasePeriods(ctx, work, id, false)
	if generationErr == nil {
		// Evaluate deferred storage invariants before releasing the savepoint.
		_, generationErr = work.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	}
	if generationErr != nil && !errors.As(generationErr, &isolated) {
		if err := work.Rollback(ctx); err != nil {
			return errors.Join(generationErr, err)
		}
	} else if err := work.Commit(ctx); err != nil {
		return err
	}
	generationErr = errors.Join(materializationErr, generationErr)
	var unresolved int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM lease_billing_period_failures f WHERE f.lease_id=$1 AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=f.lease_id AND o.kind=f.kind AND o.due_on=f.due_on) AND NOT EXISTS(SELECT 1 FROM lease_billing_period_dispositions d WHERE d.lease_id=f.lease_id AND d.kind=f.kind AND d.due_on=f.due_on AND d.source_revision=(SELECT coalesce(max(id),0) FROM lease_billing_source_changes WHERE lease_id=f.lease_id))`, id).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved > 0 && generationErr == nil {
		generationErr = &leasePeriodFailures{fmt.Errorf("%d billing periods still require retry", unresolved)}
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

func (s *Service) materializeLeasePeriods(ctx context.Context, id int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	work, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	materializationErr := s.generateLeasePeriods(ctx, work, id, true)
	if materializationErr == nil {
		_, materializationErr = work.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	}
	var isolated *leasePeriodFailures
	if materializationErr != nil && !errors.As(materializationErr, &isolated) {
		if err = work.Rollback(ctx); err != nil {
			return errors.Join(materializationErr, err)
		}
	} else if err = work.Commit(ctx); err != nil {
		return err
	}
	message := "Generation has not completed; the same obligation will be retried automatically."
	if materializationErr != nil {
		message = "Draft generation failed; review current agreement, coverage and billing terms, then retry."
	}
	if _, err = tx.Exec(ctx, `INSERT INTO lease_billing_attempts(obligation_id,failure) SELECT o.id,$2 FROM lease_billing_obligations o JOIN leases l ON l.id=o.lease_id WHERE o.lease_id=$1 AND o.due_on<=lease_dependency_today(l.company_id)+30 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id) ON CONFLICT(obligation_id) DO NOTHING`, id, message); err != nil {
		return err
	}
	state := ""
	if materializationErr != nil {
		state = message
	}
	if _, err = tx.Exec(ctx, `INSERT INTO lease_billing_worker_state(lease_id,failure) VALUES($1,$2) ON CONFLICT(lease_id) DO UPDATE SET attempted_at=clock_timestamp(),failure=CASE WHEN excluded.failure<>'' THEN excluded.failure ELSE lease_billing_worker_state.failure END`, id, state); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return materializationErr
}

// A batch may have committed valid periods and durable failures. These are not
// transaction failures: rolling back the batch would starve unrelated periods.
type leasePeriodFailures struct{ err error }

func (e *leasePeriodFailures) Error() string { return e.err.Error() }
func (e *leasePeriodFailures) Unwrap() error { return e.err }

func (s *Service) generateLeasePeriods(ctx context.Context, tx pgx.Tx, id int64, materializeOnly bool) error {
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
	var sourceRevision int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM lease_billing_source_changes WHERE lease_id=$1`, id).Scan(&sourceRevision); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT c.kind,c.due_on FROM (
 SELECT o.kind,o.due_on FROM lease_billing_obligations o WHERE o.lease_id=$1
  UNION SELECT 'monthly',d::date FROM generate_series($5::date,($2::date+30)::timestamp,interval '1 month') d WHERE $4::boolean AND d::date >= $6::date
  UNION SELECT 'coverage',(payload->>'effective_on')::date FROM lease_coverage_current WHERE lease_id=$1 AND action<>'withdraw' AND payload->>'kind'='add' AND (payload->>'charge_cents')::bigint>0 AND (payload->>'effective_on')::date >= $6::date AND $4::boolean
 ) c LEFT JOIN lease_billing_obligations o ON o.lease_id=$1 AND o.kind=c.kind AND o.due_on=c.due_on
 LEFT JOIN lease_billing_period_failures f ON f.lease_id=$1 AND f.kind=c.kind AND f.due_on=c.due_on
 WHERE c.due_on<=$2::date+30 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM lease_billing_period_dispositions d WHERE d.lease_id=$1 AND d.kind=c.kind AND d.due_on=c.due_on AND d.source_revision=$3)
 ORDER BY f.attempted_at NULLS FIRST,c.due_on,c.kind LIMIT 24`, id, today, sourceRevision, materializeOnly, anchor, cutoff)
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
	var failures []error
	for _, period := range periods {
		work, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		periodErr := func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
				return err
			}
			if period.due.Before(cutoff) {
				return ErrConflict
			}
			coveredAt := period.due
			if period.kind == "initial" {
				coveredAt = start
			}
			selected := append([]DraftLeaseTerms(nil), terms...)
			selected, eventBilling, err := currentCoverageBillingTerms(ctx, tx, id, selected, period.due, period.kind)
			if err != nil {
				return err
			}
			selectedCount := 0
			for i := range selected {
				approved := selected[i].Selected
				selected[i].Selected = eventBilling && approved
				if !approved {
					continue
				}
				for _, coverage := range base.Coverage {
					if eventBilling {
						break
					}
					day := coveredAt.Format(time.DateOnly)
					if coverage.AssetID == selected[i].AssetID && coverage.StartsOn <= day && (coverage.EndsOn == nil || *coverage.EndsOn >= day) {
						selected[i].Selected = true
					}
				}
				if !selected[i].Selected {
					continue
				}
				selectedCount++
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
				if eventBilling {
					break
				}
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
			if selectedCount == 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO lease_billing_period_dispositions(lease_id,kind,due_on,source_revision,reason) VALUES($1,$2,$3,$4,'No Assets covered at period start; no charge owed') ON CONFLICT(lease_id,kind,due_on) DO UPDATE SET source_revision=excluded.source_revision,reason=excluded.reason`, id, period.kind, period.due, sourceRevision); err != nil {
					return err
				}
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM lease_billing_period_dispositions WHERE lease_id=$1 AND kind=$2 AND due_on=$3`, id, period.kind, period.due); err != nil {
				return err
			}
			input, err := leaseObligationInput(selected, period.kind == "monthly", exempt)
			if err != nil {
				return err
			}
			obligation, financial, lines, err := materializeLeaseObligation(ctx, tx, base, selected, period.kind, period.due, input)
			if err != nil {
				return err
			}
			if materializeOnly {
				return nil
			}
			invoice, err := s.createApprovedDocument(ctx, tx, Actor{}, p, "invoice", DocumentCreation{CustomerID: base.CustomerID, Title: base.Number + " " + period.kind + " " + period.due.Format(time.DateOnly), TaxRate: input.TaxRate, LineItems: lines, Financial: financial, CustomFields: "[]", InvoiceDate: today, DueDate: period.due}, func(string) (string, error) { return "unpaid", nil }, obligation, &leaseGenerationCapability{obligation: obligation, company: company})
			if err != nil {
				return err
			}
			if err := recordLeaseGeneration(ctx, tx, id, obligation, invoice); err != nil {
				return err
			}
			return nil
		}(work)
		if periodErr == nil {
			_, periodErr = work.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		}
		if periodErr != nil {
			if err := work.Rollback(ctx); err != nil {
				return errors.Join(periodErr, err)
			}
			failures = append(failures, fmt.Errorf("%s %s: %w", period.kind, period.due.Format(time.DateOnly), periodErr))
			message := "Draft generation failed; review current agreement, coverage and billing terms, then retry."
			if _, err := tx.Exec(ctx, `INSERT INTO lease_billing_period_failures(lease_id,kind,due_on,failure) VALUES($1,$2,$3,$4) ON CONFLICT(lease_id,kind,due_on) DO UPDATE SET attempted_at=clock_timestamp(),failure=excluded.failure`, id, period.kind, period.due, message); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO lease_billing_attempts(obligation_id,failure) SELECT id,$4 FROM lease_billing_obligations WHERE lease_id=$1 AND kind=$2 AND due_on=$3 ON CONFLICT(obligation_id) DO UPDATE SET attempted_at=clock_timestamp(),failure=excluded.failure`, id, period.kind, period.due, message); err != nil {
				return err
			}
		} else {
			if err := work.Commit(ctx); err != nil {
				return err
			}
			if !materializeOnly {
				if _, err := tx.Exec(ctx, `DELETE FROM lease_billing_period_failures WHERE lease_id=$1 AND kind=$2 AND due_on=$3`, id, period.kind, period.due); err != nil {
					return err
				}
			}
		}
	}
	if len(failures) > 0 {
		return &leasePeriodFailures{errors.Join(failures...)}
	}
	return nil
}
