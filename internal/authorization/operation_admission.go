package authorization

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func operationClientKey(a Actor) string {
	if a.Channel != "api" {
		return "web"
	}
	return "api:" + a.SessionHash
}

// admitOperation runs inside the effect/receipt transaction. The unique UUID
// and immutable binding also protect callers that contend outside the shared
// authority fence. A rollback (including audit failure) releases admission.
// A matching admission is not replay authority: the domain must still load its
// immutable receipt before attempting an effect.
func admitOperation(ctx context.Context, tx pgx.Tx, a Actor, id, kind string, intent any) error {
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type,intent)
	 SELECT $1,id,$2,$3,$4,$5 FROM operation_deployment ON CONFLICT(operation_id) DO NOTHING`, id, a.UserID, operationClientKey(a), kind, raw)
	if err != nil {
		return err
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT user_id=$2 AND client_key=$3 AND operation_type=$4 AND (intent IS NULL OR intent=$5::jsonb)
	 FROM operation_admissions WHERE operation_id=$1`, id, a.UserID, operationClientKey(a), kind, raw).Scan(&same)
	if err != nil {
		return err
	}
	if !same {
		return ErrConflict
	}
	return nil
}

// AdmitOperation binds a service-owned command inside its committing transaction.
// Callers retain responsibility for their independent domain receipt and checks.
func AdmitOperation(ctx context.Context, tx pgx.Tx, a Actor, id, kind string, intent any) error {
	return admitOperation(ctx, tx, a, id, kind, intent)
}
