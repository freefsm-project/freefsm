package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func TestLeaseBillingRechecksCoverageBetweenPhases(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var pending, customer, premises, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, lease).Scan(&pending))
	historyCheck(t, pool.QueryRow(ctx, `SELECT l.customer_id,l.premises_id,d.asset_id FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id WHERE l.id=$1`, lease).Scan(&customer, &premises, &asset))
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$; CREATE FUNCTION billing_materialization_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(670401); RETURN NEW; END $$; CREATE TRIGGER billing_materialization_barrier AFTER INSERT OR UPDATE ON lease_billing_worker_state FOR EACH ROW EXECUTE FUNCTION billing_materialization_barrier()`)
	historyCheck(t, err)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(670401)`).Scan(&holder))
	svc := authorization.New(pool)
	worker, changed := make(chan error, 1), make(chan error, 1)
	go func() { worker <- svc.GenerateLeaseDrafts(ctx) }()
	commercialWaiters(t, ctx, pool, holder, 1)
	var writer int
	historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
	end := "2024-01-31"
	go func() {
		changed <- svc.WithinActor(ctx, authorization.Actor{UserID: admin}, func(tx pgx.Tx, _ authorization.Principal) error {
			return svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: lease, CustomerID: customer, PremisesID: &premises, Lifecycle: "scheduled", Coverage: []authorization.LeaseAssetDependency{{AssetID: asset, StartsOn: "2024-01-31", EndsOn: &end}}})
		})
	}()
	commercialWaiters(t, ctx, pool, writer, 1)
	historyCheck(t, barrier.Commit(ctx))
	historyCheck(t, <-changed)
	historyCheck(t, <-worker)
	var nonowed bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_billing_obligations WHERE id=$1) AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1) AND EXISTS(SELECT 1 FROM lease_billing_period_dispositions WHERE lease_id=$2 AND due_on='2024-02-01')`, pending, lease).Scan(&nonowed))
	historyEqual(t, "changed coverage suppresses the same durable intent", nonowed, true)
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-02-01'::date $$`)
	historyCheck(t, err)
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/delete", asset), nil, 303)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&count))
	historyEqual(t, "archived past-coverage Asset creates no forbidden new Invoice", count, 1)
}
