package handlers

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

func commercialLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	// Domain/authority conflicts use the ordinary review response. A failed
	// database effect or immutable audit is a server failure, never success or a
	// client-correctable validation result. Do not expose PostgreSQL diagnostics.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		internalServerError(w, r, "commercial document lifecycle", err)
		return
	}
	authorizationError(w, r, err)
}
