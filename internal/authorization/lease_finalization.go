package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/jackc/pgx/v5"
)

type LeaseFinalizationInput struct {
	DetailsRevision, BillingRevision int64
	ManagedBillingStart              string
}

type LeaseValidationError struct{ Message string }

func (e *LeaseValidationError) Error() string { return e.Message }
func (e *LeaseValidationError) Unwrap() error { return ErrConflict }

// FinalizeLease is the only initial-generation entry point. It reads approved
// Draft terms under the authority/master fence; the HTTP caller supplies neither
// Invoice content nor a privilege override. Future retry scheduling belongs to
// the billing worker, while the first not-yet-eligible month is already durable.
func (s *Service) FinalizeLease(ctx context.Context, a Actor, id int64, in LeaseFinalizationInput) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		base, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if base.Archived {
			return ErrForbidden
		}
		var old LeaseFinalizationInput
		err = tx.QueryRow(ctx, `SELECT details_revision,billing_revision,managed_billing_start::text FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&old.DetailsRevision, &old.BillingRevision, &old.ManagedBillingStart)
		if err == nil {
			if old != in {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if base.Lifecycle != "draft" {
			return ErrConflict
		}
		billing, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if base.Revision != in.DetailsRevision || billing.Revision != in.BillingRevision {
			return ErrConflict
		}
		start, e1 := time.Parse(time.DateOnly, base.StartsOn)
		end, e2 := time.Parse(time.DateOnly, base.EndsOn)
		due, e3 := time.Parse(time.DateOnly, billing.InitialDueOn)
		cutoff, e4 := time.Parse(time.DateOnly, in.ManagedBillingStart)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || end.Before(start) || due.After(start) || base.PremisesID == nil || len(base.AssetIDs) == 0 {
			return &LeaseValidationError{Message: "Complete ordered term dates, premises, Assets, managed billing start and initial due on or before commencement are required"}
		}
		var exempt bool
		var today time.Time
		if err := tx.QueryRow(ctx, `SELECT tax_exempt,lease_dependency_today(company_id) FROM customers WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL FOR SHARE`, base.CustomerID, p.CompanyID).Scan(&exempt, &today); err == pgx.ErrNoRows {
			return ErrConflict
		} else if err != nil {
			return err
		}
		var premises json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(l) FROM locations l WHERE id=$1 AND company_id=$2 AND object_type='customer' AND object_id=$3 AND deleted_at IS NULL FOR SHARE`, *base.PremisesID, p.CompanyID, base.CustomerID).Scan(&premises); err == pgx.ErrNoRows {
			return ErrConflict
		} else if err != nil {
			return err
		}
		initial, err := leaseObligationInput(billing.Terms, false, exempt)
		if err != nil {
			return err
		}
		monthly, err := leaseObligationInput(billing.Terms, true, exempt)
		if err != nil {
			return err
		}
		coverage := make([]LeaseAssetDependency, 0, len(base.AssetIDs))
		var overlap bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id) WHERE d.lease_id<>$1 AND d.asset_id=ANY($2::bigint[]) AND l.lifecycle<>'draft' AND (d.ends_on IS NULL OR d.ends_on >= $3::date))`, id, base.AssetIDs, start).Scan(&overlap); err != nil {
			return err
		}
		if overlap {
			return &LeaseValidationError{Message: "An Asset already has finalized coverage in the proposed commencement or monthly continuation interval"}
		}
		for _, asset := range base.AssetIDs {
			coverage = append(coverage, LeaseAssetDependency{AssetID: asset, StartsOn: base.StartsOn})
		}
		// Open-ended coverage includes the automatic monthly continuation, not
		// merely the fixed term. The existing seam validates all placement history.
		state := "active"
		if today.Before(start) {
			state = "scheduled"
		} else if today.After(end) {
			state = "month_to_month"
		}
		if err := s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: base.CustomerID, PremisesID: base.PremisesID, Lifecycle: state, Coverage: coverage}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE leases SET lifecycle=$2,details_revision=details_revision+1,updated_at=clock_timestamp() WHERE id=$1`, id, state); err != nil {
			return err
		}
		firstMonth := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		terms, err := json.Marshal(billing.Terms)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO lease_finalizations(lease_id,actor_id,details_revision,billing_revision,managed_billing_start,starts_on,fixed_ends_on,initial_due_on,premises_snapshot,terms,monthly_anchor) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, p.UserID, in.DetailsRevision, in.BillingRevision, cutoff, start, end, due, premises, terms, firstMonth); err != nil {
			return err
		}
		if !due.Before(cutoff) {
			if err := s.createLeaseObligation(ctx, tx, a, p, base, billing.Terms, "initial", due, today, initial); err != nil {
				return err
			}
		}
		month := firstMonth
		if month.Before(cutoff) {
			month = time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, time.UTC)
			if month.Before(cutoff) {
				month = month.AddDate(0, 1, 0)
			}
		}
		for {
			if err := s.createLeaseObligation(ctx, tx, a, p, base, billing.Terms, "monthly", month, today, monthly); err != nil {
				return err
			}
			if month.After(today.AddDate(0, 0, 30)) {
				break
			}
			month = month.AddDate(0, 1, 0)
		}
		if err := audit(ctx, tx, a, p, "lease.billing.finalize", All, "lease", id, map[string]any{"after": map[string]any{"InitialDueOn": billing.InitialDueOn, "Terms": billing.Terms, "ManagedBillingStart": in.ManagedBillingStart}}); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.finalize", All, "lease", id, map[string]any{"after": map[string]any{"StartsOn": base.StartsOn, "EndsOn": base.EndsOn, "PremisesID": base.PremisesID, "AssetIDs": base.AssetIDs, "Coverage": coverage}})
	})
}

func (s *Service) createLeaseObligation(ctx context.Context, tx pgx.Tx, a Actor, p Principal, base DraftLeaseBase, terms []DraftLeaseTerms, kind string, due, today time.Time, input commercial.Input) error {
	financial, err := commercial.Accept(input)
	if err != nil {
		return err
	}
	lines := make([]map[string]any, 0, len(input.Lines))
	for _, term := range terms {
		if !term.Selected {
			continue
		}
		line := input.Lines[len(lines)]
		lines = append(lines, map[string]any{"title": fmt.Sprintf("%s — Asset %d", base.Number, term.AssetID), "quantity": 1, "quantity_decimal": "1", "unit_price": json.Number(line.UnitPrice), "unit_price_decimal": line.UnitPrice, "taxable": line.Taxable, "lease_id": base.ID, "asset_id": term.AssetID, "period": due.Format(time.DateOnly), "charge_kind": kind})
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	var obligation int64
	if err := tx.QueryRow(ctx, `INSERT INTO lease_billing_obligations(lease_id,kind,due_on,financial,line_items) VALUES($1,$2,$3,$4,$5) RETURNING id`, base.ID, kind, due, financial, encoded).Scan(&obligation); err != nil {
		return err
	}
	if due.After(today.AddDate(0, 0, 30)) {
		return nil
	}
	invoice, err := s.createBoundDocument(ctx, tx, a, p, "invoice", DocumentCreation{CustomerID: base.CustomerID, Title: base.Number + " " + kind + " " + due.Format(time.DateOnly), TaxRate: input.TaxRate, LineItems: string(encoded), Financial: financial, CustomFields: "[]", InvoiceDate: today, DueDate: due}, func(string) (string, error) { return "unpaid", nil }, obligation)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET lease_id=$2 WHERE id=$1`, invoice, base.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO lease_invoice_generations(obligation_id,invoice_id) VALUES($1,$2)`, obligation, invoice)
	return err
}

// leaseObligationInput validates raw terms before applying Customer exemption.
// The ordinary Invoice has one document rate; zero-rate Asset lines remain
// non-taxable. Exact rational comparison avoids both textual inequality and
// float rounding that could silently combine genuinely different tax rates.
func leaseObligationInput(terms []DraftLeaseTerms, monthly, exempt bool) (commercial.Input, error) {
	in := commercial.Input{TaxRate: "0", TaxExempt: exempt}
	var common *big.Rat
	for _, term := range terms {
		if !term.Selected {
			continue
		}
		rate, ok := new(big.Rat).SetString(term.TaxRate)
		if term.InitialCents == nil || term.RentCents == nil || *term.InitialCents < 0 || *term.RentCents < 0 || !ok || !leaseTaxPattern.MatchString(term.TaxRate) || rate.Sign() < 0 || rate.Cmp(big.NewRat(100, 1)) > 0 {
			return in, &LeaseValidationError{Message: fmt.Sprintf("Asset %d requires nonnegative initial and monthly amounts and a tax rate from 0 to 100", term.AssetID)}
		}
		if !exempt && rate.Sign() > 0 {
			if common != nil && common.Cmp(rate) != 0 {
				return in, &LeaseValidationError{Message: "This obligation requires one common nonzero tax rate across taxable Assets"}
			}
			common = rate
			in.TaxRate = term.TaxRate
		}
		amount := *term.InitialCents
		if monthly {
			amount = *term.RentCents
		}
		in.Lines = append(in.Lines, commercial.Line{Quantity: "1", UnitPrice: fmt.Sprintf("%d.%02d", amount/100, amount%100), Taxable: rate.Sign() > 0})
	}
	if len(in.Lines) == 0 {
		return in, &LeaseValidationError{Message: "Select at least one Asset with complete billing terms"}
	}
	if _, err := commercial.Calculate(in); err != nil {
		return in, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return in, nil
}
