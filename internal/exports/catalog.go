package exports

import "github.com/jackc/pgx/v5/pgxpool"

// Product is the single registry shared by HTTP and background workers. Owning
// domain tickets add versioned definitions here; no worker/route split registry.
func Product(pool *pgxpool.Pool) *Service { return New(pool, Customers{}) }
