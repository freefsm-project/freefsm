package authorization

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ReplayFileBatchCommand requires a recorded batch binding. Historical NULL
// admission intent cannot prove batch intent; original nonbatch replay remains
// the owning service's responsibility. This lookup never creates admission.
func (s *Service) ReplayFileBatchCommand(ctx context.Context, a Actor, id, family string, intent any) (bool, error) {
	raw, err := json.Marshal(intent)
	if err != nil {
		return false, err
	}
	found := false
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var same bool
		err := tx.QueryRow(ctx, `SELECT coalesce(user_id=$2 AND client_key=$3 AND operation_type=$4 AND intent=$5::jsonb,false)
		 FROM operation_admissions WHERE operation_id=$1`, id, a.UserID, operationClientKey(a), family, raw).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if !same {
			return &JobExecutionConflict{Code: "operation_intent_changed"}
		}
		return nil
	})
	return found, err
}
