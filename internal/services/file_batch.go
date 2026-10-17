package services

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// SubmitBatchOperation composes the existing File service with common batch
// admission. The existing domain transaction validates dependencies at commit.
// Byte transport and verification remain independent of ordinary publication.
func (s *FileService) SubmitBatchOperation(ctx context.Context, op authorization.BatchFileOperation, dependencies func(context.Context, pgx.Tx, authorization.Actor) error) (authorization.FileReceipt, error) {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.FileReceipt{}, authorization.ErrForbidden
	}
	if s == nil {
		return authorization.FileReceipt{}, errors.New("File storage dependency is unavailable")
	}
	if op.Type == "file.rename" || op.Type == "file.remove" {
		id, err := strconv.ParseInt(op.Target, 10, 64)
		if err != nil {
			return authorization.FileReceipt{}, authorization.ErrFileOperation
		}
		p, err := r.Service.Principal(ctx, r.Actor)
		if err != nil {
			return authorization.FileReceipt{}, err
		}
		in := FileMutation{FileOperation: authorization.FileOperation{OperationID: op.OperationID, OccurredAt: op.OccurredAt, Check: dependencies}, Name: op.Name, Revision: op.Revision, ObjectType: op.ObjectType, ObjectID: op.ObjectID, Batch: &op}
		err = s.MutateFile(ctx, p.CompanyID, id, in, op.Type == "file.remove")
		var conflict *authorization.JobExecutionConflict
		if errors.As(err, &conflict) && conflict.Code == "operation_intent_changed" {
			return authorization.FileReceipt{}, err
		}
		receipt, lookupErr := r.Service.FileReceipt(ctx, r.Actor, op.OperationID)
		if lookupErr != nil {
			if err != nil {
				return authorization.FileReceipt{}, err
			}
			return authorization.FileReceipt{}, lookupErr
		}
		return receipt, err
	}
	intent := struct {
		Batch authorization.BatchFileOperation
	}{op}
	if found, err := r.Service.ReplayFileCommand(ctx, r.Actor, op.OperationID, "file.publication", intent); err != nil || found {
		if err != nil {
			return authorization.FileReceipt{}, err
		}
		return r.Service.FileReceipt(ctx, r.Actor, op.OperationID)
	}
	apply := func() error {
		in, err := s.VerifyTransfer(ctx, op.Target)
		if err != nil {
			return err
		}
		if in.ObjectType != op.ObjectType || in.ObjectID != op.ObjectID {
			return authorization.ErrConflict
		}
		p, err := r.Service.Principal(ctx, r.Actor)
		if err != nil {
			return err
		}
		ref, _ := objectref.Parse(op.ObjectType, op.ObjectID)
		reader := &transferReader{ctx: ctx, query: r.Service.Pool, id: op.Target, size: in.Size, userID: r.Actor.UserID, sessionHash: r.Actor.SessionHash}
		_, err = s.Create(ctx, p.CompanyID, ref, in.Name, "", in.Size, reader, r.Actor.UserID, FilePublication{ID: op.OperationID, Digest: in.Digest, OccurredAt: op.OccurredAt, Intent: intent, Check: func(tx pgx.Tx) error { return dependencies(ctx, tx, r.Actor) }})
		return err
	}
	err := apply()
	var conflict *authorization.JobExecutionConflict
	if errors.As(err, &conflict) && conflict.Code == "operation_intent_changed" {
		return authorization.FileReceipt{}, err
	}
	if err != nil {
		err = r.Service.RejectFilePublication(ctx, r.Actor, op.OperationID, intent, err, true)
	}
	// Validation may run outside the effect transaction. Another command can
	// acquire this UUID after our initial receipt miss but before rejection is
	// recorded. Never attach that command's receipt to the rejected intent.
	bound, bindingErr := r.Service.ReplayFileCommand(ctx, r.Actor, op.OperationID, "file.publication", intent)
	if bindingErr != nil {
		return authorization.FileReceipt{}, bindingErr
	}
	if !bound {
		if err != nil {
			return authorization.FileReceipt{}, err
		}
		return authorization.FileReceipt{}, errors.New("File publication receipt is unavailable")
	}
	receipt, lookupErr := r.Service.FileReceipt(ctx, r.Actor, op.OperationID)
	if lookupErr != nil {
		if err != nil {
			return authorization.FileReceipt{}, err
		}
		return authorization.FileReceipt{}, lookupErr
	}
	// A concurrent same-intent acceptance is authoritative even if this attempt
	// subsequently observed parent/byte loss. It is not a second failed effect.
	if receipt.Code == "accepted" {
		return receipt, nil
	}
	return receipt, err
}

// Preserve the old publication digest shape for every installed nonbatch path.
func publicationIntent(identity FilePublication, original []any) any {
	if identity.Intent != nil {
		return identity.Intent
	}
	return original
}

func publicationIntentJSON(identity FilePublication, original []any) ([]byte, error) {
	return json.Marshal(publicationIntent(identity, original))
}
