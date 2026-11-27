package authorization_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestListStateIsolationDurabilityAndDenial(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role)
		SELECT 7101,id,'list-admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
		INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id)
		SELECT 7102,id,'list-second@test','Second','hash','tech',4 FROM companies LIMIT 1;
		INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(7102,'list-session','web',clock_timestamp()+interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: 7102, SessionHash: "list-session", Channel: "web"}
	var auditBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"q": {"private literal % _ 界"}, "page": {"7"}, "state": {"active"}}
	saved, err := svc.SaveListState(ctx, actor, "search", values, 0)
	if err != nil || saved.Revision != 1 || saved.Values.Has("page") {
		t.Fatalf("save: %+v %v", saved, err)
	}
	restarted := authorization.New(pool)
	got, err := restarted.LoadListState(ctx, actor, "search")
	if err != nil || got.Revision != 1 || got.Values.Get("q") != values.Get("q") {
		t.Fatalf("durability: %+v %v", got, err)
	}
	for _, key := range []string{"customers", "leases", "items"} {
		got, err := svc.LoadListState(ctx, actor, key)
		if err != nil || got.Revision != 0 || len(got.Values) != 0 {
			t.Fatalf("list isolation: %+v %v", got, err)
		}
	}
	got, err = svc.LoadListState(ctx, authorization.Actor{UserID: 7101}, "search")
	if err != nil || got.Revision != 0 {
		t.Fatalf("user isolation: %+v %v", got, err)
	}
	for _, denied := range []authorization.Actor{
		{UserID: 7102, SessionHash: "wrong"}, {UserID: 999999},
		{UserID: 7102, Channel: "api"}, {UserID: 7102, Channel: "unknown"},
	} {
		if _, err := svc.LoadListState(ctx, denied, "search"); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("load denial: %v", err)
		}
		if _, err := svc.SaveListState(ctx, denied, "search", values, 1); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("save denial: %v", err)
		}
		if _, err := svc.ResetListState(ctx, denied, "search", 1); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("reset denial: %v", err)
		}
	}
	if _, err := svc.LoadListState(ctx, actor, "reports"); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("unknown list: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE token_hash='list-session'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveListState(ctx, actor, "search", values, 1); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("revoked session saved: %v", err)
	}
	// A current role change does not authorize a saved query, nor remove the
	// user's intrinsic ability to load it for validation/reset.
	actor.SessionHash = ""
	reset, err := svc.ResetListState(ctx, actor, "search", 1)
	if err != nil || reset.Revision != 2 || len(reset.Values) != 0 {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	if _, err := svc.SaveListState(ctx, actor, "search", values, 1); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("stale resurrection: %v", err)
	}
	got, err = restarted.LoadListState(ctx, actor, "search")
	if err != nil || got.Revision != 2 || len(got.Values) != 0 {
		t.Fatalf("reset not durable: %+v %v", got, err)
	}
	var auditAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatalf("presentation audited: %d -> %d: %v", auditBefore, auditAfter, err)
	}
	var originalCompany, otherCompany int64
	if err := pool.QueryRow(ctx, `SELECT company_id FROM users WHERE id=7102`).Scan(&originalCompany); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other company','list-state-other') RETURNING id`).Scan(&otherCompany); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET company_id=$1 WHERE id=7102`, otherCompany); err != nil {
		t.Fatal(err)
	}
	got, err = svc.LoadListState(ctx, actor, "search")
	if err != nil || got.Revision != 0 || len(got.Values) != 0 {
		t.Fatalf("old company state leaked: %+v %v", got, err)
	}
	if _, err := svc.SaveListState(ctx, actor, "search", url.Values{"q": {"other company"}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET company_id=$1 WHERE id=7102`, originalCompany); err != nil {
		t.Fatal(err)
	}
	got, err = svc.LoadListState(ctx, actor, "search")
	if err != nil || got.Revision != 2 || len(got.Values) != 0 {
		t.Fatalf("company state overwritten: %+v %v", got, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active=false WHERE id=7102`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoadListState(ctx, actor, "search"); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("disabled actor loaded state: %v", err)
	}
}

func TestListStateStorageFailureAndInvalidWrites(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role)
		SELECT id,'list-failure@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user}
	for _, invalid := range []url.Values{
		{"q": {strings.Repeat("界", 501)}}, {"q": {"a", "b"}},
		{"filter": {strings.Repeat("a", 16385)}}, {"q": {"\xff"}},
	} {
		got, err := svc.SaveListState(ctx, actor, "search", invalid, 0)
		if !errors.Is(err, authorization.ErrConflict) || got.Revision != 0 || got.Values != nil {
			t.Fatalf("invalid storage input: %+v %v", got, err)
		}
	}
	if _, err := svc.ResetListState(ctx, actor, "search", -1); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("negative revision: %v", err)
	}
	if _, err := svc.SaveListState(ctx, actor, "search", url.Values{"q": {"retained"}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION list_state_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected preference failure'; END $$;
		CREATE TRIGGER list_state_test_fail BEFORE UPDATE ON list_states FOR EACH ROW EXECUTE FUNCTION list_state_test_fail();`); err != nil {
		t.Fatal(err)
	}
	for _, reset := range []bool{false, true} {
		var got authorization.ListState
		var err error
		if reset {
			got, err = svc.ResetListState(ctx, actor, "search", 1)
		} else {
			got, err = svc.SaveListState(ctx, actor, "search", url.Values{"q": {"failed"}}, 1)
		}
		if err == nil || errors.Is(err, authorization.ErrConflict) || got.Revision != 0 || got.Values != nil {
			t.Fatalf("storage failure reported success/partial state: %+v %v", got, err)
		}
	}
	got, err := svc.LoadListState(ctx, actor, "search")
	if err != nil || got.Revision != 1 || got.Values.Get("q") != "retained" {
		t.Fatalf("failed write changed state: %+v %v", got, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE list_states RENAME TO list_states_unavailable`); err != nil {
		t.Fatal(err)
	}
	got, err = svc.LoadListState(ctx, actor, "search")
	if err == nil || got.Values != nil || got.Revision != 0 {
		t.Fatalf("missing storage reported empty success: %+v %v", got, err)
	}
}

func TestListStateConcurrentCASAndReset(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role)
		SELECT id,'list-concurrency@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user}
	// Both tabs observed absence. Exactly one insertion can win.
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"tab one", "tab two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.SaveListState(ctx, actor, "search", url.Values{"q": {text}}, 0)
			outcomes <- err
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	winners, conflicts := 0, 0
	for err := range outcomes {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, authorization.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d", winners, conflicts)
	}
	// A request admitted before Reset holds revision 1. Release its save only
	// after Reset commits, using channels rather than timing assumptions.
	admitted, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		old, err := svc.LoadListState(ctx, actor, "search")
		close(admitted)
		<-release
		if err == nil {
			_, err = svc.SaveListState(ctx, actor, "search", url.Values{"q": {"stale"}}, old.Revision)
		}
		done <- err
	}()
	<-admitted
	if _, err := svc.ResetListState(ctx, actor, "search", 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("admitted save resurrected reset: %v", err)
	}
	reset, err := svc.ResetListState(ctx, actor, "items", 0)
	if err != nil || reset.Revision != 1 {
		t.Fatalf("absent reset: %+v %v", reset, err)
	}
	if _, err := svc.SaveListState(ctx, actor, "items", url.Values{"q": {"stale absent"}}, 0); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("absent reset resurrected: %v", err)
	}
}
