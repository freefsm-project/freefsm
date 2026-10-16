package authorization

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

type FileReceipt struct {
	OperationID string    `json:"operation_id"`
	FileID      int64     `json:"file_id,omitempty"`
	EffectID    string    `json:"effect_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

// Reconciliation never loads a File or its original parent's representation.
func (s *Service) FileReceipt(ctx context.Context, a Actor, id string) (FileReceipt, error) {
	r := FileReceipt{OperationID: id}
	if _, err := uuid.Parse(id); err != nil {
		return r, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT file_id,code,accepted_at FROM file_command_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3
		UNION ALL SELECT p.file_id,'accepted',f.created_at FROM file_publications p JOIN files f ON f.id=p.file_id WHERE p.operation_id=$1 AND p.user_id=$2 AND (p.client_key=$4 OR p.client_key=(SELECT 'api:'||id::text FROM sessions WHERE user_id=$2 AND token_hash=$5 AND kind='mobile'))
		UNION ALL SELECT m.file_id,'accepted',m.accepted_at FROM file_mutation_operations m WHERE m.operation_id=$1 AND m.user_id=$2 AND m.client_key=$5 AND NOT EXISTS(SELECT 1 FROM file_command_receipts WHERE operation_id=$1)
		LIMIT 1`, id, a.UserID, operationClientKey(a), map[bool]string{true: "web", false: ""}[a.Channel != "api"], a.SessionHash).Scan(&r.FileID, &r.Code, &r.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err == nil && r.Code == "accepted" && r.FileID > 0 {
		r.EffectID = strconv.FormatInt(r.FileID, 10)
	}
	return r, err
}
