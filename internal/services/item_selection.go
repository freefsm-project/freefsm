package services

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
)

type catalogTransactionKey struct{}

func checkItemSelections(ctx context.Context, tx authorization.SelectionTransaction, kind string, id int64, lines string) error {
	if r, ok := authorization.FromContext(ctx); ok {
		return r.Service.CheckItemSelections(ctx, r.Actor, tx, kind, id, lines)
	}
	return nil
}

// The selection transaction still holds the authority mutex here. Re-reading
// also catches time-based session expiry while waiting for a domain row lock.
func recheckSelectionActor(ctx context.Context) error {
	if r, ok := authorization.FromContext(ctx); ok {
		_, err := r.Service.Principal(ctx, r.Actor)
		return err
	}
	return nil
}

// An outer transaction keeps the complete commercial edit and Item selection
// evidence atomic; recursive service calls reuse its client, never another pool.
func catalogWrite[T any](ctx context.Context, client *ent.Client, kind string, id int64, lines []LineItem, fn func(context.Context, *ent.Client) (T, error)) (T, error) {
	var zero T
	tx, err := client.Tx(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	raw, err := EncodeLineItems(lines)
	if err != nil {
		return zero, err
	}
	if err := checkItemSelections(ctx, tx, kind, id, raw); err != nil {
		return zero, err
	}
	result, err := fn(context.WithValue(ctx, catalogTransactionKey{}, true), tx.Client())
	if err != nil {
		return zero, err
	}
	if err := recheckSelectionActor(ctx); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	switch entity := any(result).(type) {
	case *ent.Estimate:
		entity.Unwrap()
	case *ent.Invoice:
		entity.Unwrap()
	}
	return result, nil
}

func needsCatalogTransaction(ctx context.Context) bool {
	_, ok := authorization.FromContext(ctx)
	return ok && ctx.Value(catalogTransactionKey{}) == nil
}
