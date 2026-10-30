package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type LeaseChoice struct {
	ID    int64
	Label string
}
type LeaseChoices struct {
	Premises, Assets                   []LeaseChoice
	CanSelectAgreement, CanViewBilling bool
}

func (s *Service) DraftLeaseChoices(ctx context.Context, a Actor, id int64) (LeaseChoices, error) {
	var out LeaseChoices
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) {
			return ErrNotFound
		}
		base, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		out.CanSelectAgreement = p.Allows("file.download", All)
		out.CanViewBilling = p.Allows("lease.billing.view", All)
		for _, kind := range []string{"location", "asset"} {
			if !p.Allows(kind+".view", All) {
				continue
			}
			query := `SELECT id,title FROM locations WHERE company_id=$1 AND object_type='customer' AND object_id=$2 AND deleted_at IS NULL ORDER BY title,id`
			args := []any{p.CompanyID, base.CustomerID}
			if kind == "asset" {
				query = `SELECT id,number||' — '||name||CASE WHEN customer_id IS NULL THEN ' — unplaced' WHEN customer_id<>$2 THEN ' — placed with another Customer (unavailable for coverage)' WHEN location_id IS DISTINCT FROM $3::bigint THEN ' — different premises' ELSE '' END FROM assets WHERE company_id=$1 AND ownership_class='company_owned' AND deleted_at IS NULL ORDER BY number,id`
				args = []any{p.CompanyID, base.CustomerID, base.PremisesID}
			}
			rows, err := tx.Query(ctx, query, args...)
			if err != nil {
				return err
			}
			choices := []LeaseChoice{}
			for rows.Next() {
				var c LeaseChoice
				if err := rows.Scan(&c.ID, &c.Label); err != nil {
					rows.Close()
					return err
				}
				choices = append(choices, c)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if kind == "location" {
				out.Premises = choices
			} else {
				out.Assets = choices
			}
		}
		return nil
	})
	return out, err
}
