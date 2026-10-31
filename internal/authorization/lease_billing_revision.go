package authorization

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A revision changes rent/tax, never initial allocation, dates or coverage.
type LeaseBillingChange struct {
	AssetID   int64  `json:"asset_id"`
	RentCents int64  `json:"rent_cents"`
	TaxRate   string `json:"tax_rate"`
}
type LeaseBillingRevisionInput struct {
	ExpectedRevision int64                `json:"expected_revision"`
	EffectiveOn      string               `json:"effective_on"`
	Reason           string               `json:"reason"`
	OperationKey     string               `json:"operation_key"`
	Changes          []LeaseBillingChange `json:"changes"`
}
type LeaseBillingRevisionRecord struct {
	ID, ActorID                     int64
	EffectiveOn, Reason, RecordedAt string
	Before, After                   []LeaseBillingChange
}
type LeaseBillingRevisionReview struct {
	ID, Revision int64
	Number       string
	CanManage    bool
	History      []LeaseBillingRevisionRecord
	Assets       []int64
	Original     []DraftLeaseTerms
}

func readLeaseBillingRevisions(ctx context.Context, tx pgx.Tx, id int64) ([]LeaseBillingRevisionRecord, error) {
	rows, err := tx.Query(ctx, `SELECT id,actor_id,effective_on::text,reason,recorded_at::text,before_value,after_value FROM lease_billing_revisions WHERE lease_id=$1 ORDER BY effective_on,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaseBillingRevisionRecord
	for rows.Next() {
		var r LeaseBillingRevisionRecord
		var before, after []byte
		if err = rows.Scan(&r.ID, &r.ActorID, &r.EffectiveOn, &r.Reason, &r.RecordedAt, &before, &after); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(before, &r.Before); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(after, &r.After); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) ReviewLeaseBillingRevisions(ctx context.Context, a Actor, id int64) (LeaseBillingRevisionReview, error) {
	var out LeaseBillingRevisionReview
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) {
			return ErrNotFound
		}
		billing, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if !billing.Finalized {
			return ErrConflict
		}
		out.ID = id
		out.Number = billing.Number
		out.CanManage = !billing.Archived && p.Allows("lease.billing.manage", All)
		out.History, err = readLeaseBillingRevisions(ctx, tx, id)
		for _, r := range out.History {
			if r.ID > out.Revision {
				out.Revision = r.ID
			}
		}
		if err != nil {
			return err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT terms FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &out.Original); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1 UNION SELECT (t->>'AssetID')::bigint FROM lease_finalizations f CROSS JOIN LATERAL jsonb_array_elements(f.terms) t WHERE f.lease_id=$1 ORDER BY 1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var asset int64
			if err = rows.Scan(&asset); err != nil {
				return err
			}
			out.Assets = append(out.Assets, asset)
		}
		return rows.Err()
	})
	return out, err
}

func applyLeaseBillingRevisions(terms []DraftLeaseTerms, history []LeaseBillingRevisionRecord, day string) []DraftLeaseTerms {
	out := append([]DraftLeaseTerms(nil), terms...)
	for _, r := range history {
		if r.EffectiveOn > day {
			continue
		}
		for _, change := range r.After {
			for i := range out {
				if out[i].AssetID == change.AssetID {
					rent := change.RentCents
					out[i].RentCents = &rent
					out[i].TaxRate = change.TaxRate
					out[i].BillingRevisionID = r.ID
				}
			}
		}
	}
	return out
}

func (s *Service) ReviseLeaseBilling(ctx context.Context, a Actor, id int64, in LeaseBillingRevisionInput) error {
	return s.transactOutcome(ctx, a, false, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		billing, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if billing.Archived || !billing.Finalized {
			return ErrConflict
		}
		invalid := func(m string) error { return &LeaseValidationError{Message: m} }
		if strings.TrimSpace(in.Reason) == "" || len(in.OperationKey) < 16 || len(in.OperationKey) > 128 {
			return invalid("A reason and submission identity are required")
		}
		if _, err = time.Parse(time.DateOnly, in.EffectiveOn); err != nil {
			return invalid("A valid effective date is required")
		}
		if len(in.Changes) == 0 {
			return invalid("At least one Asset rent and tax change is required")
		}
		intent, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT actor_id=$2 AND lease_id=$3 AND intent=$4::jsonb FROM lease_billing_revisions WHERE operation_key=$1`, in.OperationKey, p.UserID, id, intent).Scan(&same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		history, err := readLeaseBillingRevisions(ctx, tx, id)
		if err != nil {
			return err
		}
		var revision int64
		for _, r := range history {
			if r.ID > revision {
				revision = r.ID
			}
		}
		if revision != in.ExpectedRevision {
			return ErrConflict
		}
		base, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		var exempt, eligible bool
		var raw []byte
		var due string
		err = tx.QueryRow(ctx, `SELECT c.tax_exempt,c.deleted_at IS NULL AND l.deleted_at IS NULL AND l.company_id=c.company_id AND l.object_type='customer' AND l.object_id=c.id,f.terms,f.initial_due_on::text FROM lease_finalizations f JOIN leases a ON a.id=f.lease_id JOIN customers c ON c.id=a.customer_id JOIN locations l ON l.id=a.premises_id WHERE f.lease_id=$1 FOR SHARE OF c,l`, id).Scan(&exempt, &eligible, &raw, &due)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrConflict
		}
		if err = tx.QueryRow(ctx, `SELECT NOT archived AND customer_id=$2 AND premises_id IS NOT DISTINCT FROM $3 AND lifecycle<>'draft' FROM lease_master_dependencies WHERE lease_id=$1 FOR SHARE`, id, base.CustomerID, base.PremisesID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrConflict
		}
		for _, c := range base.Coverage {
			if _, err = tx.Exec(ctx, `SELECT lease_asset_timeline_validate($1)`, c.AssetID); err != nil {
				return err
			}
		}
		var original []DraftLeaseTerms
		if err = json.Unmarshal(raw, &original); err != nil {
			return err
		}
		day, _ := time.Parse(time.DateOnly, in.EffectiveOn)
		// Review the applicable full-period terms, including already scheduled coverage.
		terms, _, err := currentCoverageBillingTerms(ctx, tx, id, original, day, "monthly")
		if err != nil {
			return err
		}
		var before []LeaseBillingChange
		seen := map[int64]bool{}
		for _, change := range in.Changes {
			if seen[change.AssetID] {
				return invalid("Each Asset may appear only once")
			}
			seen[change.AssetID] = true
			found := false
			for _, term := range terms {
				if term.AssetID == change.AssetID && term.Selected && term.RentCents != nil {
					before = append(before, LeaseBillingChange{term.AssetID, *term.RentCents, term.TaxRate})
					found = true
				}
			}
			if !found {
				return invalid("Select an Asset with applicable approved billing terms on the effective date")
			}
			if err = tx.QueryRow(ctx, `SELECT company_id=$2 AND deleted_at IS NULL AND ownership_class='company_owned' FROM assets WHERE id=$1 FOR SHARE`, change.AssetID, p.CompanyID).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return ErrConflict
			}
			zero := int64(0)
			if _, err = leaseObligationInput([]DraftLeaseTerms{{AssetID: change.AssetID, Selected: true, InitialCents: &zero, RentCents: &change.RentCents, TaxRate: change.TaxRate}}, true, exempt); err != nil {
				return err
			}
		}
		beforeRaw, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterRaw, err := json.Marshal(in.Changes)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_billing_revisions(lease_id,actor_id,operation_key,intent,effective_on,reason,before_value,after_value) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, p.UserID, in.OperationKey, intent, in.EffectiveOn, in.Reason, beforeRaw, afterRaw); err != nil {
			return err
		}
		if err = validateLeaseBillingSchedule(ctx, tx, id, original, exempt, due); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT record_lease_billing_source_change($1,$2::date,NULL,$3)`, id, in.EffectiveOn, "Billing revision: "+in.Reason); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.billing.revise", All, "lease", id, map[string]any{"effective_on": in.EffectiveOn, "reason": in.Reason, "before": before, "after": in.Changes})
	})
}

// Membership and pricing writers validate the same effective configurations.
// Generated periods are snapshots, not candidates for retrospective repricing.
func validateLeaseBillingSchedule(ctx context.Context, tx pgx.Tx, id int64, original []DraftLeaseTerms, exempt bool, initialDue string) error {
	var cutoff, firstMonth time.Time
	if err := tx.QueryRow(ctx, `SELECT f.managed_billing_start,(date_trunc('month',l.starts_on)+interval '1 month')::date FROM lease_finalizations f JOIN leases l ON l.id=f.lease_id WHERE f.lease_id=$1`, id).Scan(&cutoff, &firstMonth); err != nil {
		return err
	}
	if firstMonth.Before(cutoff) {
		firstMonth = time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, time.UTC)
		if firstMonth.Before(cutoff) {
			firstMonth = firstMonth.AddDate(0, 1, 0)
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT 'monthly',period_on::date FROM (SELECT (date_trunc('month',starts_on)+interval '1 month')::date AS period_on FROM leases WHERE id=$1 UNION ALL SELECT (date_trunc('month',effective_on)+interval '1 month')::date FROM lease_billing_revisions WHERE lease_id=$1 UNION ALL SELECT effective_on FROM lease_billing_revisions WHERE lease_id=$1 AND extract(day FROM effective_on)=1 UNION ALL SELECT (date_trunc('month',(payload->>'effective_on')::date)+interval '1 month')::date FROM lease_coverage_current WHERE lease_id=$1 AND action<>'withdraw') d UNION SELECT 'coverage',(payload->>'effective_on')::date FROM lease_coverage_current WHERE lease_id=$1 AND action<>'withdraw' AND payload->>'kind'='add' AND (payload->>'charge_cents')::bigint>0 UNION SELECT 'initial',$2::date`, id, initialDue)
	if err != nil {
		return err
	}
	type candidate struct {
		kind string
		due  time.Time
	}
	var checks []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.kind, &c.due); err != nil {
			rows.Close()
			return err
		}
		checks = append(checks, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range checks {
		if c.kind == "monthly" && c.due.Before(firstMonth) {
			c.due = firstMonth
		}
		if c.due.Before(cutoff) {
			continue
		}
		// The first ungenerated month in each configuration is sufficient; later
		// changes supply their own candidates. The loop is bounded by saved invoices.
		skip := false
		for {
			var generated bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=$1 AND o.kind=$2 AND o.due_on=$3)`, id, c.kind, c.due).Scan(&generated); err != nil {
				return err
			}
			if !generated {
				break
			}
			if c.kind != "monthly" {
				skip = true
				break
			}
			c.due = c.due.AddDate(0, 1, 0)
		}
		if skip {
			continue
		}
		selected, _, err := currentCoverageBillingTerms(ctx, tx, id, original, c.due, c.kind)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			continue
		}
		if _, err = leaseObligationInput(selected, c.kind == "monthly", exempt); err != nil {
			return err
		}
	}
	return nil
}
