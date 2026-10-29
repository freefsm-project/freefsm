package authorization

import (
	"context"
	"math/big"
	"regexp"
	"time"

	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

type DraftLeaseTerms struct {
	AssetID                 int64
	InitialCents, RentCents *int64
	TaxRate                 string
	Selected                bool
	AssetArchived           bool
}

// Billing has its own DTO and queries: it never loads base details/collaboration.
type DraftLeaseBilling struct {
	Lifecycle            string
	Finalized            bool
	ID, Revision         int64
	Number, InitialDueOn string
	CanManage            bool
	Archived             bool
	LifecycleRevision    int64
	Terms                []DraftLeaseTerms
	GenerationFailure    string
	Periods              []LeaseBillingHistoryRow
	Query                LeaseBillingQuery
	Total                int
}

func readLeaseBilling(ctx context.Context, tx pgx.Tx, p Principal, id int64) (DraftLeaseBilling, error) {
	var out DraftLeaseBilling
	err := tx.QueryRow(ctx, `SELECT l.id,l.number_label,coalesce(b.revision,0),coalesce(b.initial_due_on::text,''),l.deleted_at IS NOT NULL,l.lifecycle_revision FROM leases l LEFT JOIN lease_draft_billing b ON b.lease_id=l.id WHERE l.id=$1 AND l.company_id=$2 FOR UPDATE OF l`, id, p.CompanyID).Scan(&out.ID, &out.Number, &out.Revision, &out.InitialDueOn, &out.Archived, &out.LifecycleRevision)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=$1)`, id).Scan(&out.Finalized); err != nil {
		return out, err
	}
	if out.Archived && !p.Allows("lease.archived.view", All) {
		return out, ErrNotFound
	}
	calendar, err := readLeaseCalendar(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out.Lifecycle = calendar.Lifecycle
	rows, err := tx.Query(ctx, `SELECT ids.asset_id,t.initial_cents,t.rent_cents,coalesce(t.tax_rate::text,''),EXISTS(SELECT 1 FROM lease_intended_assets i WHERE i.lease_id=$1 AND i.asset_id=ids.asset_id),EXISTS(SELECT 1 FROM assets a WHERE a.id=ids.asset_id AND a.deleted_at IS NOT NULL) FROM (SELECT asset_id FROM lease_intended_assets WHERE lease_id=$1 UNION SELECT asset_id FROM lease_draft_asset_terms WHERE lease_id=$1) ids LEFT JOIN lease_draft_asset_terms t ON t.lease_id=$1 AND t.asset_id=ids.asset_id ORDER BY ids.asset_id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var term DraftLeaseTerms
		if err := rows.Scan(&term.AssetID, &term.InitialCents, &term.RentCents, &term.TaxRate, &term.Selected, &term.AssetArchived); err != nil {
			return out, err
		}
		out.Terms = append(out.Terms, term)
	}
	return out, rows.Err()
}

func (s *Service) DraftLeaseBilling(ctx context.Context, a Actor, id int64, options ...LeaseBillingQuery) (DraftLeaseBilling, error) {
	var out DraftLeaseBilling
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) {
			return ErrNotFound
		}
		var err error
		out, err = readLeaseBilling(ctx, tx, p, id)
		out.CanManage = !out.Archived && !out.Finalized && out.Lifecycle == "draft" && p.Allows("lease.billing.manage", All)
		if err != nil {
			return err
		}
		query := LeaseBillingQuery{}
		if len(options) > 0 {
			query = options[0]
		}
		out.Query, err = NormalizeLeaseBillingQuery(query)
		if err != nil {
			return err
		}
		if err != nil || !out.Finalized || out.Archived {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT failure FROM lease_billing_worker_state WHERE lease_id=$1),'')`, id).Scan(&out.GenerationFailure); err != nil {
			return err
		}
		return readLeaseBillingHistory(ctx, tx, p.CompanyID, &out)
	})
	return out, err
}

// Preserve the same exact decimal percentage vocabulary as commercial inputs;
// Draft storage must not introduce an artificial rounding scale.
var leaseTaxPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

func (s *Service) SaveDraftLeaseBilling(ctx context.Context, a Actor, id, revision int64, due string, in DraftLeaseTerms, lifecycleRevision int64) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		// The no-write proof ends before the first prospective-terms INSERT,
		// not after an audit, commit, or transport failure.
		writeDispatched := false
		defer func() {
			if !writeDispatched {
				saveaction.RejectBeforeWrite(ctx)
			}
		}()
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		old, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if old.Revision != revision {
			return ErrConflict
		}
		if old.Archived {
			return ErrForbidden
		}
		if old.Finalized || old.Lifecycle != "draft" {
			return ErrConflict
		}
		if old.LifecycleRevision != lifecycleRevision {
			return ErrConflict
		}
		active, err := leaseCustomerActive(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
		if due != "" {
			if _, err := time.Parse(time.DateOnly, due); err != nil {
				return ErrConflict
			}
		}
		for _, v := range []*int64{in.InitialCents, in.RentCents} {
			if v != nil && *v < 0 {
				return ErrConflict
			}
		}
		if in.TaxRate != "" {
			rate, ok := new(big.Rat).SetString(in.TaxRate)
			if !ok || !leaseTaxPattern.MatchString(in.TaxRate) || rate.Cmp(big.NewRat(100, 1)) > 0 {
				return ErrConflict
			}
		}
		var selected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_intended_assets WHERE lease_id=$1 AND asset_id=$2)`, id, in.AssetID).Scan(&selected); err != nil {
			return err
		}
		if !selected {
			return ErrConflict
		}
		writeDispatched = true
		if _, err := tx.Exec(ctx, `INSERT INTO lease_draft_billing(lease_id,revision,initial_due_on) VALUES($1,1,NULLIF($2,'')::date) ON CONFLICT(lease_id) DO UPDATE SET revision=lease_draft_billing.revision+1,initial_due_on=EXCLUDED.initial_due_on`, id, due); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO lease_draft_asset_terms(lease_id,asset_id,initial_cents,rent_cents,tax_rate) VALUES($1,$2,$3,$4,NULLIF($5,'')::numeric) ON CONFLICT(lease_id,asset_id) DO UPDATE SET initial_cents=EXCLUDED.initial_cents,rent_cents=EXCLUDED.rent_cents,tax_rate=EXCLUDED.tax_rate`, id, in.AssetID, in.InitialCents, in.RentCents, in.TaxRate); err != nil {
			return err
		}
		after, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.billing.change", All, "lease", id, map[string]any{"before": old, "after": after})
	})
}

func leaseCustomerActive(ctx context.Context, tx pgx.Tx, p Principal, id int64) (bool, error) {
	var customer int64
	if err := tx.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&customer); err != nil {
		return false, err
	}
	return customerLifecycleState(ctx, tx, p, customer, true)
}
