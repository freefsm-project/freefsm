package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type LeaseBillingQuery struct {
	Page, PerPage int
	Filter        string
}
type LeaseBillingHistoryRow struct {
	ObligationID         int64
	Kind, DueOn, Failure string
	ReviewRequired       bool
}

// Match the existing Lease list's bounds/defaults. Filters are a fixed allowlist,
// never SQL fragments; all navigation carries these normalized values.
func NormalizeLeaseBillingQuery(q LeaseBillingQuery) (LeaseBillingQuery, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Page > 1000000 {
		return q, ErrConflict
	}
	if q.PerPage != 25 && q.PerPage != 50 && q.PerPage != 100 {
		q.PerPage = 25
	}
	if q.Filter != "review" && q.Filter != "failed" {
		q.Filter = "all"
	}
	return q, nil
}

// Include failed candidates that have not yet materialized an obligation. UNION
// keeps the (Lease, kind, due date) identity unique when a later retry does so.
// Count and rows share one authorized, fenced transaction. The list loads only
// an existence flag, not an unbounded array of historical review reasons.
const leaseBillingHistoryCTE = `WITH allowed AS (
 SELECT id FROM leases WHERE id=$1 AND company_id=$2
), keys AS (
 SELECT o.kind,o.due_on FROM lease_billing_obligations o JOIN allowed l ON l.id=o.lease_id
 UNION SELECT f.kind,f.due_on FROM lease_billing_period_failures f JOIN allowed l ON l.id=f.lease_id
), periods AS (
 SELECT coalesce(o.id,0) obligation_id,k.kind,k.due_on,
 EXISTS(SELECT 1 FROM lease_invoice_generations g JOIN lease_invoice_review_flags r ON r.invoice_id=g.invoice_id WHERE g.obligation_id=o.id) review_required,
 CASE WHEN NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM lease_billing_period_dispositions d WHERE d.lease_id=$1 AND d.kind=k.kind AND d.due_on=k.due_on AND d.source_revision=(SELECT coalesce(max(id),0) FROM lease_billing_source_changes WHERE lease_id=$1))
 THEN coalesce(f.failure,'') ELSE '' END failure
 FROM keys k LEFT JOIN lease_billing_obligations o ON o.lease_id=$1 AND o.kind=k.kind AND o.due_on=k.due_on
 LEFT JOIN lease_billing_period_failures f ON f.lease_id=$1 AND f.kind=k.kind AND f.due_on=k.due_on
) `

const leaseBillingHistoryFilter = ` WHERE ($3='all' OR ($3='review' AND review_required) OR ($3='failed' AND failure<>''))`

func readLeaseBillingHistory(ctx context.Context, tx pgx.Tx, company int64, out *DraftLeaseBilling) error {
	if err := tx.QueryRow(ctx, leaseBillingHistoryCTE+`SELECT count(*) FROM periods`+leaseBillingHistoryFilter, out.ID, company, out.Query.Filter).Scan(&out.Total); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, leaseBillingHistoryCTE+`SELECT obligation_id,kind,due_on::text,review_required,failure FROM periods`+leaseBillingHistoryFilter+` ORDER BY due_on DESC,obligation_id DESC,kind LIMIT $4 OFFSET $5`, out.ID, company, out.Query.Filter, out.Query.PerPage, (out.Query.Page-1)*out.Query.PerPage)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row LeaseBillingHistoryRow
		if err := rows.Scan(&row.ObligationID, &row.Kind, &row.DueOn, &row.ReviewRequired, &row.Failure); err != nil {
			return err
		}
		out.Periods = append(out.Periods, row)
	}
	return rows.Err()
}
