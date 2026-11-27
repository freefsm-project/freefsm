package handlers

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

func discoveryWaitBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker, count int) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var n int
		historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid))`, pool.Config().ConnConfig.RuntimeParams["application_name"], blocker).Scan(&n))
		if n >= count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected real PostgreSQL waiters not observed")
		case <-tick.C:
		}
	}
}

func TestDiscoveryQueuedGrantRevocationHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f.client.Item.Create().SetCompanyID(f.company).SetName("Revocation106").SaveX(ctx)
	_, role, cookie := f.reader(t, "QueuedRevoke", authorization.Grant{Permission: "item.view", Scope: authorization.All})
	f.search(t, cookie, "q=Revocation106")
	tx, err := f.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var blocker int
	historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
	revoked := make(chan error, 1)
	go func() {
		_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "QueuedRevoke", nil, 1)
		revoked <- err
	}()
	discoveryWaitBlocked(t, ctx, f.pool, blocker, 1)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- discoveryCall(f.web, cookie, "GET", "/search?q=Revocation106") }()
	discoveryWaitBlocked(t, ctx, f.pool, blocker, 2)
	historyCheck(t, tx.Commit(ctx))
	select {
	case err := <-revoked:
		historyCheck(t, err)
	case <-ctx.Done():
		t.Fatal("revocation blocked")
	}
	select {
	case w := <-response:
		discoveryEqual(t, w.Code, 403)
		assertNotContains(t, w.Body.String(), "data-record-id=")
	case <-ctx.Done():
		t.Fatal("search blocked")
	}
	requestBody(t, f.web, cookie, "GET", "/search?q=Revocation106", 403)
}

func TestDiscoveryAssignmentSnapshotCountAndRowsHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Snapshot owner").SaveX(ctx)
	uid, _, cookie := f.reader(t, "Snapshot106", authorization.Grant{Permission: "job.view", Scope: authorization.Assigned})
	for range 31 {
		j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Snapshot106").SaveX(ctx)
		f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	}
	discoveryEqual(t, f.search(t, cookie, "q=Snapshot106").Total, 31)
	tx, err := f.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var blocker int
	historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker))
	_, err = tx.Exec(ctx, `LOCK TABLE custom_field_definitions IN ACCESS EXCLUSIVE MODE`)
	historyCheck(t, err)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- discoveryCall(f.web, cookie, "GET", "/search?q=Snapshot106") }()
	discoveryWaitBlocked(t, ctx, f.pool, blocker, 1)
	var reader int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid))`, f.pool.Config().ConnConfig.RuntimeParams["application_name"], blocker).Scan(&reader))
	deleted := make(chan error, 1)
	go func() {
		_, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, uid)
		deleted <- err
	}()
	discoveryWaitBlocked(t, ctx, f.pool, reader, 1)
	historyCheck(t, tx.Commit(ctx))
	select {
	case w := <-done:
		discoveryEqual(t, w.Code, 200)
		o := discoveryObserve(t, w.Body.String())
		if o.Total != 31 || len(o.IDs) != 25 {
			t.Fatalf("mixed read snapshots: %+v", o)
		}
	case <-ctx.Done():
		t.Fatal("search blocked")
	}
	select {
	case err := <-deleted:
		historyCheck(t, err)
	case <-ctx.Done():
		t.Fatal("unassignment blocked")
	}
	discoveryEqual(t, f.search(t, cookie, "q=Snapshot106").Total, 0)
}
