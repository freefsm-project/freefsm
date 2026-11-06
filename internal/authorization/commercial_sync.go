package authorization

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SyncCommercial uses independent document View authority. Job linkage bounds
// the field surface but never introduces a Job View or pricing prerequisite.
func (s *Service) SyncCommercial(ctx context.Context, a Actor, kind string, in SyncRequest) (SyncPage, error) {
	if (kind != "estimate" && kind != "invoice") || in.PageSize < 1 || in.PageSize > 200 || (in.Scope != "assigned" && in.Scope != "all") || a.SessionHash == "" {
		return SyncPage{}, ErrSyncResnapshot
	}
	scope := Assigned
	if in.Scope == "all" {
		scope = All
	}
	in.Scope = kind + "s:" + in.Scope
	return s.syncRecords(ctx, a, in, kind+".view", scope, func(ctx context.Context, tx pgx.Tx, p Principal, a Actor, _ string) ([]syncEntry, error) {
		rows, err := tx.Query(ctx, `SELECT d.id,d.job_id,d.customer_id FROM `+kind+`s d JOIN jobs j ON j.id=d.job_id AND j.company_id=d.company_id WHERE d.company_id=$1 AND d.deleted_at IS NULL AND d.conversion_hidden_at IS NULL AND j.deleted_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2)) ORDER BY d.id`, p.CompanyID, p.UserID, scope == All)
		if err != nil {
			return nil, err
		}
		type target struct{ id, job, customer int64 }
		var targets []target
		for rows.Next() {
			var v target
			if err := rows.Scan(&v.id, &v.job, &v.customer); err != nil {
				rows.Close()
				return nil, err
			}
			targets = append(targets, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out := make([]syncEntry, 0, len(targets))
		for _, v := range targets {
			if _, err := parentAccess(ctx, tx, p, kind, v.id, "", false); err != nil {
				return nil, err
			}
			data, err := readCommercial(ctx, tx, p, a, kind, v.id)
			if err != nil {
				return nil, err
			}
			id, err := syncIdentity(ctx, tx, kind, v.id)
			if err != nil {
				return nil, err
			}
			data["id"], data["legacy_id"], data["customer_id"] = id, v.id, v.customer
			data, err = syncJSONObject(data)
			if err != nil {
				return nil, err
			}
			assigned, err := assignedJobAccess(ctx, tx, p.UserID, v.job, false)
			if err != nil {
				return nil, err
			}
			_, pricing := data["line_items"]
			out = append(out, syncEntry{InstalledID: v.id, ID: id, Anchor: fmt.Sprintf("%d:%d:%t:%t", v.job, v.customer, assigned, pricing), Data: data})
		}
		return out, nil
	})
}
