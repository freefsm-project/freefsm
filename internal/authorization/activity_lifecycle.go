package authorization

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Lifecycle writers own their eligibility checks and locks. These snapshots
// feed the same immutable audit/activity transaction as details commands.
func lifecycleSnapshot(ctx context.Context, tx pgx.Tx, p Principal, kind string, id int64) (json.RawMessage, error) {
	table, ok := activityTables[kind]
	if !ok {
		return nil, ErrNotFound
	}
	var snapshot json.RawMessage
	err := tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM `+table+` r WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&snapshot)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return snapshot, err
}

func auditLifecycle(ctx context.Context, tx pgx.Tx, a Actor, p Principal, kind string, id int64, action string, before json.RawMessage, detail map[string]any) error {
	after, err := lifecycleSnapshot(ctx, tx, p, kind, id)
	if err != nil {
		return err
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["before"], detail["after"] = before, after
	return audit(ctx, tx, a, p, kind+"."+action, All, kind, id, detail)
}
