package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// ReplayFileCommand checks an existing binding without creating an admission for
// a new operation. New admission remains atomic with its effect or rejection.
func (s *Service) ReplayFileCommand(ctx context.Context, a Actor, id, family string, intent any) (bool, error) {
	found := false
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_admissions WHERE operation_id=$1)`, id).Scan(&found); err != nil || !found {
			return err
		}
		return admitOperation(ctx, tx, a, id, family, intent)
	})
	return found, err
}
