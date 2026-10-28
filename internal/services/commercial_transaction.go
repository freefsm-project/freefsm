package services

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
)

type commercialTransactionKey struct{}

// Context-free service adapters remain trusted domain writers, not fabricated
// product principals. They still need the same lifecycle serialization as web
// and field commands. Number allocation, calculations and model-store writes all
// use this transaction; no preflight-only check or independently committed step.
func needsCommercialTransaction(ctx context.Context) bool {
	_, authorized := authorization.FromContext(ctx)
	return !authorized && ctx.Value(commercialTransactionKey{}) == nil
}

func commercialWrite[T any](ctx context.Context, client *ent.Client, write func(context.Context, *ent.Client) (*T, error)) (*T, error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return nil, err
	}
	result, err := write(context.WithValue(ctx, commercialTransactionKey{}, tx), tx.Client())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	switch entity := any(result).(type) {
	case *ent.Invoice:
		entity.Unwrap()
	case *ent.Estimate:
		entity.Unwrap()
	}
	return result, nil
}

func commercialWriteCustomer(ctx context.Context, kind string, id, nextCustomer int64) error {
	tx, ok := ctx.Value(commercialTransactionKey{}).(*ent.Tx)
	if !ok {
		return nil
	} // Product adapters own their Details/Creation transaction.
	if id != 0 {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(customer_id,0) FROM `+kind+`s WHERE id=$1 FOR UPDATE`, id).Scan(&current); err != nil {
			return err
		}
		if err := authorization.CommercialCustomerForWrite(ctx, tx, current); err != nil {
			return err
		}
	}
	if nextCustomer != 0 {
		return authorization.CommercialCustomerForWrite(ctx, tx, nextCustomer)
	}
	return nil
}
