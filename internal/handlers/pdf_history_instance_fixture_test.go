package handlers

import (
	"net/http"
	"testing"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This current-only composition is deliberately not copied into the exact old
// producer archive: that binary must retain its historical APIs and behavior.
func init() {
	pdfHistoricalInstanceDSN = testutil.InstanceDatabase
	pdfHistoricalInstanceRouter = func(t *testing.T, pool *pgxpool.Pool, client *ent.Client) http.Handler {
		f := &controlledMailInstance{pool: pool, client: client, sessions: services.NewSessionService(pool)}
		f.start(t)
		return f.api
	}
}
