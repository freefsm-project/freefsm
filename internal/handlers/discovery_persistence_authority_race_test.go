package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Reauthorization must cover the preference commit, not only a prior read.
// The revoker queues behind the POST's real related-Customer validation and
// ahead of any subsequent preference transaction. No production hook is used.
func TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Commit authority owner").SaveX(ctx)
	lease, err := authorization.New(f.pool).RegisterDraftLease(ctx, authorization.Actor{UserID: f.admin}, customer.ID)
	historyCheck(t, err)
	historyCheck(t, authorization.New(f.pool).SaveDraftLeaseDetails(ctx, authorization.Actor{UserID: f.admin}, lease.ID, authorization.DraftLeaseDetailsInput{Title: "CommitAuthority106"}))
	uid, role, cookie := f.reader(t, "CommitAuthority106", authorization.Grant{Permission: "lease.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	_, err = f.pool.Exec(ctx, `CREATE TABLE discovery_commit_order(id bigserial PRIMARY KEY,kind text NOT NULL);
	CREATE FUNCTION discovery_capture_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO discovery_commit_order(kind) VALUES(TG_ARGV[0]); RETURN NEW; END $$;
	CREATE TRIGGER discovery_capture_preference BEFORE INSERT OR UPDATE ON list_states FOR EACH ROW EXECUTE FUNCTION discovery_capture_commit('state');
	CREATE TRIGGER discovery_capture_revocation BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION discovery_capture_commit('revoke');`)
	historyCheck(t, err)
	tx, err := f.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var holder int
	historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder))
	_, err = tx.Exec(ctx, `LOCK TABLE customers IN ACCESS EXCLUSIVE MODE`)
	historyCheck(t, err)
	saved := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		saved <- discoverySave(f, cookie, fmt.Sprintf("q=CommitAuthority106&type=lease&customer=%d", customer.ID), 0)
	}()
	discoveryWaitBlocked(t, ctx, f.pool, holder, 1)
	var reader int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid))`, f.pool.Config().ConnConfig.RuntimeParams["application_name"], holder).Scan(&reader))
	revoked := make(chan error, 1)
	go func() {
		_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "CommitAuthority106", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}, 1)
		revoked <- err
	}()
	discoveryWaitBlocked(t, ctx, f.pool, reader, 1)
	historyCheck(t, tx.Commit(ctx))
	select {
	case err := <-revoked:
		historyCheck(t, err)
	case <-ctx.Done():
		t.Fatal("revocation blocked")
	}
	var status int
	select {
	case w := <-saved:
		status = w.Code
		if status != 403 && status != 204 {
			t.Fatalf("preference commit: status=%d body=%s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("preference response blocked")
	}
	var order string
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT coalesce(string_agg(kind,',' ORDER BY id),'') FROM discovery_commit_order`).Scan(&order))
	if status == 204 && order != "state,revoke" {
		t.Fatalf("unauthorized state committed after queued revocation: status=%d order=%s", status, order)
	}
	if status == 403 && order != "revoke" {
		t.Fatalf("denied save left effects: %s", order)
	}
	state, err := authorization.New(f.pool).LoadListState(ctx, authorization.Actor{UserID: uid}, "search")
	historyCheck(t, err)
	wantRevision := int64(0)
	if status == 204 {
		wantRevision = 1
	}
	discoveryEqual(t, state.Revision, wantRevision)
	discoveryReset(t, f, cookie, state.Revision, 303)
	reset, err := authorization.New(f.pool).LoadListState(ctx, authorization.Actor{UserID: uid}, "search")
	historyCheck(t, err)
	discoveryEqual(t, len(reset.Values), 0)
	discoveryEqual(t, reset.Revision, state.Revision+1)
}
