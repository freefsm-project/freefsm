package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// auditOperationalEvent declares the operational effect separately from the
// permission used to authorize it. Facts can be reduced changed-field objects:
// neither tenant identity nor event classification is inferred from snapshots.
// The database projects this audit exactly once in the same transaction.
func auditOperationalEvent(ctx context.Context, tx pgx.Tx, a Actor, p Principal, permission string, scope Scope, typ string, id int64, event string, detail map[string]any) error {
	detail["operational_event"] = map[string]any{
		"company_id":  p.CompanyID,
		"target_type": typ,
		"target_id":   id,
		"action":      event,
	}
	return audit(ctx, tx, a, p, permission, scope, typ, id, detail)
}
