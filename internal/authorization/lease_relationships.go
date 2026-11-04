package authorization

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type RelatedLease struct {
	ID                              int64
	Number, Title, StartsOn, EndsOn string
	Archived, CanOpen, CanBilling   bool
}
type RelatedLeases struct {
	Visible  bool
	Kind     string
	SourceID int64
	Total    int
	Rows     []RelatedLease
}

// RelatedLeases is a source-authorized, nonrecursive base projection. Financial
// values and Files stay on their independently authorized owning surfaces.
func (s *Service) RelatedLeases(ctx context.Context, a Actor, kind string, id int64) (RelatedLeases, error) {
	out := RelatedLeases{Kind: kind, SourceID: id}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		if kind != "customer" && kind != "asset" && kind != "location" {
			return ErrNotFound
		}
		if !p.Allows(kind+".view", All) {
			return ErrNotFound
		}
		var archived bool
		err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM `+pgx.Identifier{kind + "s"}.Sanitize()+` WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&archived)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if archived && !p.Allows(kind+".archived.view", All) {
			return ErrNotFound
		}
		if !p.Allows("lease.view", All) {
			return nil
		}
		out.Visible = true
		where := ` FROM leases l WHERE l.company_id=$1 AND l.customer_id=$2`
		if kind == "location" {
			where = ` FROM leases l WHERE l.company_id=$1 AND l.premises_id=$2`
		}
		if kind == "asset" {
			where = ` FROM leases l WHERE l.company_id=$1 AND EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=l.id AND d.asset_id=$2)`
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, p.CompanyID, id).Scan(&out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT l.id,l.number_label,l.title,l.deleted_at IS NOT NULL`+where+` ORDER BY l.document_number,l.id LIMIT 25`, p.CompanyID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row RelatedLease
			if err := rows.Scan(&row.ID, &row.Number, &row.Title, &row.Archived); err != nil {
				rows.Close()
				return err
			}
			row.CanOpen = !row.Archived || p.Allows("lease.archived.view", All)
			row.CanBilling = row.CanOpen && p.Allows("lease.billing.view", All)
			out.Rows = append(out.Rows, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if kind == "asset" {
			var coverage []RelatedLease
			for _, row := range out.Rows {
				if !row.CanOpen {
					coverage = append(coverage, row)
					continue
				}
				spans, err := tx.Query(ctx, `SELECT starts_on::text,coalesce(ends_on::text,'Ongoing') FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=$2 ORDER BY starts_on`, row.ID, id)
				if err != nil {
					return err
				}
				for spans.Next() {
					copy := row
					if err := spans.Scan(&copy.StartsOn, &copy.EndsOn); err != nil {
						spans.Close()
						return err
					}
					coverage = append(coverage, copy)
				}
				err = spans.Err()
				spans.Close()
				if err != nil {
					return err
				}
			}
			out.Rows = coverage
		}
		return nil
	})
	return out, err
}

func (r RelatedLeases) URL() string {
	key := r.Kind
	if key == "location" {
		key = "premises"
	}
	return fmt.Sprintf("/leases?%s=%d", key, r.SourceID)
}
