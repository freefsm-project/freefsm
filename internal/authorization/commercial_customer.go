package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CommercialCustomerForWrite is a domain invariant, not a grant. Callers must
// already hold the shared authority fence and own the transaction containing
// their entire write. History reads, archive and admitted transport evidence do
// not call this content/creation boundary.
func CommercialCustomerForWrite(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, customerID int64) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 FOR SHARE`, customerID).Scan(&active); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if !active {
		return ErrConflict
	}
	return nil
}

func commercialDocumentCustomerForWrite(ctx context.Context, tx pgx.Tx, kind string, id int64) error {
	var customer int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(customer_id,0) FROM `+kind+`s WHERE id=$1`, id).Scan(&customer); err != nil {
		return err
	}
	return CommercialCustomerForWrite(ctx, tx, customer)
}
