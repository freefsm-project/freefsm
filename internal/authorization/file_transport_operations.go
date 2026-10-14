package authorization

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FileTransportCommand struct {
	FileOperation
	TransferID string
	Kind       string
	Intent     any
	Validate   func() error
}

type FileTransportReceipt struct {
	OperationID string    `json:"operation_id"`
	TransferID  string    `json:"transfer_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

func fileTransportCode(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrConflict):
		return "file_conflict"
	case errors.Is(err, ErrFileOperation):
		return "invalid_file"
	case strings.HasPrefix(err.Error(), "invalid"), strings.HasPrefix(err.Error(), "file extension"), strings.HasPrefix(err.Error(), "file size"):
		return "invalid_file"
	default:
		return ""
	}
}

func fileTransportError(code string) error {
	switch code {
	case "accepted":
		return nil
	case "not_found":
		return ErrNotFound
	case "forbidden":
		return ErrForbidden
	case "file_conflict":
		return ErrConflict
	default:
		return ErrFileOperation
	}
}

func (s *Service) FileTransportReceipt(ctx context.Context, a Actor, id string) (FileTransportReceipt, error) {
	r := FileTransportReceipt{OperationID: id}
	if _, err := uuid.Parse(id); err != nil {
		return r, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT transfer_id,code,accepted_at FROM file_transport_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, id, a.UserID, a.Channel+":"+a.SessionHash).Scan(&r.TransferID, &r.Code, &r.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return r, err
}

// Transport effects and receipts share the same transaction. A transfer ID is
// only an effect identity; every start/chunk carries its own operation UUID.
func fileTransportOperation(ctx context.Context, tx pgx.Tx, a Actor, in FileTransportCommand, apply func(pgx.Tx) error) error {
	if err := admitOperation(ctx, tx, a, in.OperationID, "file.transport."+in.Kind, in.Intent); err != nil {
		return err
	}
	var code string
	err := tx.QueryRow(ctx, `SELECT code FROM file_transport_receipts WHERE operation_id=$1`, in.OperationID).Scan(&code)
	if err == nil {
		return fileTransportError(code)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = in.Validate()
	if err == nil {
		err = apply(sp)
	}
	code = fileTransportCode(err)
	if err != nil {
		_ = sp.Rollback(ctx)
		if code == "" {
			return err
		}
	} else if err = sp.Commit(ctx); err != nil {
		return err
	}
	effect := ""
	if code == "accepted" {
		effect = in.TransferID
	}
	_, err = tx.Exec(ctx, `INSERT INTO file_transport_receipts(operation_id,user_id,client_key,transfer_id,code) VALUES($1,$2,$3,$4,$5)`, in.OperationID, a.UserID, a.Channel+":"+a.SessionHash, effect, code)
	return err
}
