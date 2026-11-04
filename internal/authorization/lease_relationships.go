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
	Visible       bool
	Kind          string
	SourceID      int64
	Total         int
	Rows          []RelatedLease
	Current       *RelatedLease
	Page, PerPage int
}

type LeaseCoveragePage struct{ Page, PerPage int }

// RelatedLeases is a source-authorized, nonrecursive base projection. Financial
// values and Files stay on their independently authorized owning surfaces.
func (s *Service) RelatedLeases(ctx context.Context, a Actor, kind string, id int64, pages ...LeaseCoveragePage) (RelatedLeases, error) {
	out := RelatedLeases{Kind: kind, SourceID: id, Page: 1, PerPage: 25}
	if len(pages) > 0 {
		out.Page, out.PerPage = pages[0].Page, pages[0].PerPage
		if out.Page < 1 {
			out.Page = 1
		}
		if out.PerPage != 25 && out.PerPage != 50 && out.PerPage != 100 {
			out.PerPage = 25
		}
	}
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
		if out.Page > 1000000 {
			return ErrConflict
		}
		if kind == "asset" {
			return out.readAssetCoverage(ctx, tx, p)
		}
		where := ` FROM leases l WHERE l.company_id=$1 AND l.customer_id=$2`
		if kind == "location" {
			where = ` FROM leases l WHERE l.company_id=$1 AND l.premises_id=$2`
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
		return nil
	})
	return out, err
}

// Page intervals rather than Lease identities. A restricted archived agreement
// contributes one bounded reference, never its interval count or date ordering.
func (out *RelatedLeases) readAssetCoverage(ctx context.Context, tx pgx.Tx, p Principal) error {
	const history = `WITH coverage AS (
 SELECT l.id,l.document_number,l.number_label,l.title,l.deleted_at IS NOT NULL AS archived,d.starts_on,d.ends_on,true AS can_open
 FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id
 WHERE l.company_id=$1 AND d.asset_id=$2 AND (l.deleted_at IS NULL OR $3)
 UNION ALL
 SELECT l.id,l.document_number,l.number_label,l.title,true,NULL::date,NULL::date,false
 FROM leases l WHERE l.company_id=$1 AND l.deleted_at IS NOT NULL AND NOT $3
 AND EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=l.id AND d.asset_id=$2)
 ) `
	args := []any{p.CompanyID, out.SourceID, p.Allows("lease.archived.view", All)}
	if err := tx.QueryRow(ctx, history+`SELECT count(*) FROM coverage`, args...).Scan(&out.Total); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, history+`SELECT id,number_label,title,archived,can_open,coalesce(starts_on::text,''),CASE WHEN can_open THEN coalesce(ends_on::text,'Ongoing') ELSE '' END FROM coverage ORDER BY document_number,id,starts_on LIMIT $4 OFFSET $5`, append(args, out.PerPage, (out.Page-1)*out.PerPage)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var row RelatedLease
		if err := rows.Scan(&row.ID, &row.Number, &row.Title, &row.Archived, &row.CanOpen, &row.StartsOn, &row.EndsOn); err != nil {
			rows.Close()
			return err
		}
		row.CanBilling = row.CanOpen && p.Allows("lease.billing.view", All)
		out.Rows = append(out.Rows, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Non-overlap guarantees at most one current finalized interval. This query
	// is independent of the history page and uses inclusive company dates.
	var current RelatedLease
	err = tx.QueryRow(ctx, `SELECT l.id,l.number_label,l.title,d.starts_on::text,coalesce(d.ends_on::text,'Ongoing')
 FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id
 WHERE l.company_id=$1 AND d.asset_id=$2 AND l.deleted_at IS NULL AND l.lifecycle<>'draft' AND NOT lease_has_ended(l.id)
 AND d.starts_on<=lease_dependency_today(l.company_id) AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id))
 ORDER BY l.document_number,l.id,d.starts_on LIMIT 1`, p.CompanyID, out.SourceID).Scan(&current.ID, &current.Number, &current.Title, &current.StartsOn, &current.EndsOn)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	current.CanOpen, current.CanBilling = true, p.Allows("lease.billing.view", All)
	out.Current = &current
	return nil
}

func (r RelatedLeases) CoverageURL(page int) string {
	return fmt.Sprintf("/assets/%d?coverage_page=%d&coverage_per_page=%d#lease-coverage", r.SourceID, page, r.PerPage)
}

func (r RelatedLeases) URL() string {
	key := r.Kind
	if key == "location" {
		key = "premises"
	}
	return fmt.Sprintf("/leases?%s=%d", key, r.SourceID)
}
