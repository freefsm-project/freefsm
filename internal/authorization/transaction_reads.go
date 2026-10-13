package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transactionReadsKey struct{}
type transactionReads struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

// WithTransactionReads binds purpose-limited reads to the command's existing
// fenced transaction. It is scoped to this Service's pool and must not escape
// the callback. Acquiring another connection while holding the authority fence
// can deadlock a saturated pool; it also misses the command's own prior writes.
func (s *Service) WithTransactionReads(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, transactionReadsKey{}, transactionReads{s.Pool, tx})
}

func (s *Service) reads(ctx context.Context) queryer {
	if bound, ok := ctx.Value(transactionReadsKey{}).(transactionReads); ok && bound.pool == s.Pool && bound.tx != nil {
		return bound.tx
	}
	return s.Pool
}
