package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CommentCommand struct {
	OperationID string     `json:"operation_id"`
	Content     string     `json:"content,omitempty"`
	ReplyTo     int64      `json:"reply_to,omitempty"`
	RemoveID    int64      `json:"-"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}
type CommentReceipt struct {
	OperationID string    `json:"operation_id"`
	CommentID   int64     `json:"comment_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

func commentCode(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrConflict):
		return "comment_conflict"
	case errors.Is(err, ErrCommentInvalid):
		return "invalid_comment"
	default:
		return ""
	}
}
func commentError(code string) error {
	switch code {
	case "not_found":
		return ErrNotFound
	case "forbidden":
		return ErrForbidden
	case "comment_conflict":
		return ErrConflict
	case "invalid_comment":
		return ErrCommentInvalid
	}
	return nil
}

// SubmitComment is the shared online/offline acceptance seam. It never follows
// conversion or alters intent. Terminal rejections are durable; storage/audit
// failures roll back the receipt and remain retryable under the same operation ID.
func (s *Service) SubmitComment(ctx context.Context, a Actor, ref objectref.Ref, in CommentCommand) (CommentReceipt, error) {
	result := CommentReceipt{OperationID: in.OperationID}
	op, err := uuid.Parse(in.OperationID)
	if err != nil || op == uuid.Nil {
		return result, ErrCommentInvalid
	}
	in.OperationID = op.String()
	result.OperationID = in.OperationID
	encoded, _ := json.Marshal(struct {
		Type    string
		ID      int64
		Command CommentCommand
	}{string(ref.Type), ref.ID, in})
	// RemoveID is intentionally absent from the wire command, but part of intent.
	digest := fmt.Sprintf("%x", sha256.Sum256(append(encoded, []byte(fmt.Sprintf(":remove:%d", in.RemoveID))...)))
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, in.OperationID, "comment", digest); err != nil {
			return err
		}
		var savedDigest, client string
		err := tx.QueryRow(ctx, `SELECT intent_hash,client_key,coalesce(comment_id,0),code,accepted_at FROM comment_operation_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, in.OperationID).Scan(&savedDigest, &client, &result.CommentID, &result.Code, &result.AcceptedAt)
		if err == nil {
			if savedDigest != digest || client != a.Channel+":"+a.SessionHash {
				return ErrConflict
			}
			outcome = commentError(result.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.ReplyTo < 0 || in.RemoveID < 0 || in.OccurredAt != nil && in.OccurredAt.After(time.Now()) {
			outcome = ErrCommentInvalid
		} else {
			result.CommentID, outcome = applyComment(ctx, tx, p, a, ref, in)
		}
		result.Code = commentCode(outcome)
		if result.Code == "" {
			return outcome
		}
		if outcome != nil {
			result.CommentID = 0
		}
		return tx.QueryRow(ctx, `INSERT INTO comment_operation_receipts(user_id,operation_id,client_key,intent_hash,comment_id,code) VALUES($1,$2,$3,$4,nullif($5,0),$6) RETURNING accepted_at`, a.UserID, in.OperationID, a.Channel+":"+a.SessionHash, digest, result.CommentID, result.Code).Scan(&result.AcceptedAt)
	})
	if err != nil {
		return CommentReceipt{}, err
	}
	return result, outcome
}

func (s *Service) CommentReceipt(ctx context.Context, a Actor, operationID string) (CommentReceipt, error) {
	result := CommentReceipt{OperationID: operationID}
	if _, err := uuid.Parse(operationID); err != nil {
		return result, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT coalesce(comment_id,0),code,accepted_at FROM comment_operation_receipts WHERE user_id=$1 AND operation_id=$2 AND client_key=$3`, a.UserID, operationID, a.Channel+":"+a.SessionHash).Scan(&result.CommentID, &result.Code, &result.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return result, err
}
