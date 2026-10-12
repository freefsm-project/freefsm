package database

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CustomerMasterPreflight is a read-only observation of legacy masters. It never
// infers earlier owners, chooses a primary, merges duplicates or updates kind.
func (db *DB) CustomerMasterPreflight(ctx context.Context) (string, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var report string
	err = tx.QueryRow(ctx, `SELECT jsonb_pretty(jsonb_build_object(
 'format_version',1,
 'customers',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'display_name',display_name,'recorded_kind',account_type,'kind_review',CASE WHEN account_type NOT IN ('individual','company') OR (account_type='individual' AND company_name<>'') OR (account_type='company' AND company_name='') THEN 'explicit review required; no inferred kind' ELSE NULL END) ORDER BY id) FROM customers),'[]'::jsonb),
 'contacts',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'recorded_customer_id',customer_id,'ownership_history_unrecorded','current ownership is not evidence of previous owners') ORDER BY id) FROM customer_contacts),'[]'::jsonb),
 'locations',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'recorded_object_type',object_type,'recorded_customer_id',object_id,'recorded_primary',is_primary,'ownership_history_unrecorded','current ownership is not evidence of previous owners') ORDER BY id) FROM locations),'[]'::jsonb),
 'duplicate_primary',coalesce((SELECT jsonb_agg(x) FROM (SELECT object_id customer_id,array_agg(id ORDER BY id) location_ids FROM locations WHERE object_type='customer' AND is_primary GROUP BY object_id HAVING count(*)>1 ORDER BY object_id)x),'[]'::jsonb),
 'ownership_link_review',coalesce((SELECT jsonb_agg(x) FROM (SELECT j.id job_id,j.customer_id,j.customer_contact_id,j.location_id FROM jobs j LEFT JOIN customer_contacts c ON c.id=j.customer_contact_id LEFT JOIN locations l ON l.id=j.location_id WHERE (j.customer_contact_id IS NOT NULL AND c.customer_id IS DISTINCT FROM j.customer_id) OR (j.location_id IS NOT NULL AND (l.object_type IS DISTINCT FROM 'customer' OR l.object_id IS DISTINCT FROM j.customer_id)) ORDER BY j.id)x),'[]'::jsonb),
 'review','Review ambiguous kind and current ownership/link discrepancies explicitly. Duplicate names, email, phone and addresses are valid, never merge them. Historical ownership cannot be reconstructed from current rows.'
 ))::text`).Scan(&report)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return report, nil
}
