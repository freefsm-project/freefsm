package authorization

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RejectFilePublication records only a terminal, identifiable pre-publication
// domain rejection. Storage/audit failures and identity misuse remain untouched.
func (s *Service) RejectFilePublication(ctx context.Context, a Actor, id string, intent any, cause error) error {
	code := fileTransportCode(cause)
	if code == "" || code == "accepted" || code == "file_conflict" {
		return cause
	}
	if key, err := uuid.Parse(id); err != nil || key == uuid.Nil {
		return cause
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, id, "file.publication", intent); err != nil {
			return err
		}
		// An accepted publication's later representation failure cannot rewrite
		// its original disposition.
		_, err := tx.Exec(ctx, `INSERT INTO file_command_receipts(operation_id,user_id,client_key,file_id,code)
		 SELECT $1,$2,$3,0,$4 WHERE NOT EXISTS(SELECT 1 FROM file_publications WHERE operation_id=$1)
		 ON CONFLICT(operation_id) DO NOTHING`, id, a.UserID, operationClientKey(a), code)
		return err
	})
	if err != nil {
		return err
	}
	return cause
}

func FilePublicationRejection(ctx context.Context, tx pgx.Tx, id string) error {
	var code string
	err := tx.QueryRow(ctx, `SELECT code FROM file_command_receipts WHERE operation_id=$1`, id).Scan(&code)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return fileTransportError(code)
}
