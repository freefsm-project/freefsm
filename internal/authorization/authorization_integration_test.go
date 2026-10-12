package authorization_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func authorizationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("authz_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		root.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		root.Close()
	})
	return pool
}

func fixture(t *testing.T, legacy ...bool) (*pgxpool.Pool, *authorization.Service) {
	t.Helper()
	pool := authorizationPool(t)
	ctx := context.Background()
	db := &database.DB{Pool: pool}
	if len(legacy) > 0 && legacy[0] {
		old := fstest.MapFS{}
		entries, err := fs.ReadDir(database.MigrationFS(), ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() > "054_backup_activity.up.sql" {
				continue
			}
			b, err := fs.ReadFile(database.MigrationFS(), e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
		if err := db.Migrate(ctx, old); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 1001,id,'legacy-admin@test','Recorded admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
 INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 1002,id,'legacy-dispatch@test','Recorded dispatcher','x','dispatcher' FROM companies LIMIT 1;
 INSERT INTO customers(id,company_id,display_name,notes) SELECT 1003,id,'Preserved customer','Preserved internal notes' FROM companies LIMIT 1;
 INSERT INTO jobs(id,company_id,customer_id,job_type) SELECT 1004,id,1003,'Preserved job' FROM companies LIMIT 1;`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	return pool, authorization.New(pool)
}

func TestReviewedUpgradeBlocksUnresolvedMappingsAndPreservesFacts(t *testing.T) {
	pool, svc := fixture(t, true)
	ctx := context.Background()
	admin := authorization.Actor{UserID: 1001}
	if _, err := svc.Principal(ctx, admin); !errors.Is(err, authorization.ErrPending) {
		t.Fatalf("unreviewed Customer cutover accessible: %v", err)
	}
	if err := svc.ApproveUser(ctx, authorization.Actor{UserID: 1002}, 1002, 1, ""); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("nonadmin approved authority: %v", err)
	}
	if err := svc.Cutover(ctx, admin); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("unresolved cutover: %v", err)
	}
	role, err := svc.SaveRole(ctx, admin, 0, "Reviewed empty role", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := approveCurrent(ctx, svc, admin, 1001, 1); err != nil {
		t.Fatal(err)
	}
	if err := approveCurrent(ctx, svc, admin, 1002, role); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, admin, role, "Changed since review", nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cutover(ctx, admin); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("stale grant review: %v", err)
	}
	if err := approveCurrent(ctx, svc, admin, 1002, role); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cutover(ctx, admin); err != nil {
		t.Fatal(err)
	}
	// A new module instance represents restart: authority is database state,
	// rather than a name, a cached role decision or a process-local review flag.
	restarted := authorization.New(pool)
	p, err := restarted.Principal(ctx, authorization.Actor{UserID: 1002})
	if err != nil || len(p.Grants) != 0 || p.Protected {
		t.Fatalf("invented historical grants: %+v %v", p, err)
	}
	var label, notes string
	var customerID int64
	if err := pool.QueryRow(ctx, `SELECT u.role,c.notes,j.customer_id FROM users u CROSS JOIN customers c CROSS JOIN jobs j WHERE u.id=1002 AND c.id=1003 AND j.id=1004`).Scan(&label, &notes, &customerID); err != nil || label != "dispatcher" || notes != "Preserved internal notes" || customerID != 1003 {
		t.Fatalf("upgrade rewrote recorded facts: %s %s %d %v", label, notes, customerID, err)
	}
	if _, err := restarted.SaveRole(ctx, authorization.Actor{UserID: 1002}, 0, "Minted", nil); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("legacy discriminator remained authority: %v", err)
	}
	// Verify in a fresh OS process and freshly opened connection, not just a
	// replacement service sharing this process's memory or connection pool.
	u, err := url.Parse(os.Getenv("FREEFSM_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	u.RawQuery = q.Encode()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestAuthorizationRestartProbe$", "-test.v")
	command.Env = append(os.Environ(), "FREEFSM_AUTH_RESTART_URL="+u.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh process: %v %s", err, output)
	}
}

func approveCurrent(ctx context.Context, s *authorization.Service, a authorization.Actor, user, role int64) error {
	review, err := s.Review(ctx, a)
	if err != nil {
		return err
	}
	return s.ApproveUser(ctx, a, user, role, review.Snapshot)
}

func TestApprovalRejectsGrantsChangedAfterDisplayedReview(t *testing.T) {
	_, svc := fixture(t, true)
	ctx := context.Background()
	admin := authorization.Actor{UserID: 1001}
	role, err := svc.SaveRole(ctx, admin, 0, "Reviewed role", nil)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := svc.Review(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, admin, role, "Reviewed role", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveUser(ctx, admin, 1002, role, shown.Snapshot); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("approved unreviewed grants: %v", err)
	}
}

func TestAuthorizationRestartProbe(t *testing.T) {
	dsn := os.Getenv("FREEFSM_AUTH_RESTART_URL")
	if dsn == "" {
		t.Skip("subprocess restart probe")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := authorization.New(pool)
	admin, err := svc.Principal(ctx, authorization.Actor{UserID: 1001})
	if err != nil || !admin.Protected {
		t.Fatalf("protected authority lost across restart: %+v %v", admin, err)
	}
	p, err := svc.Principal(ctx, authorization.Actor{UserID: 1002})
	if err != nil || len(p.Grants) != 0 {
		t.Fatalf("empty Role changed across restart: %+v %v", p, err)
	}
	var linked int64
	if err := pool.QueryRow(ctx, `SELECT customer_id FROM jobs WHERE id=1004`).Scan(&linked); err != nil || linked != 1003 {
		t.Fatalf("relationship lost across restart: %d %v", linked, err)
	}
}

func TestConcurrentLegacyDemotionCannotRemoveLastProtectedAdministrator(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"disable", `UPDATE users SET is_active=false WHERE id=$1`},
		{"demote", `UPDATE users SET authorization_role_id=4 WHERE id=$1`},
		{"invalidate credential", `UPDATE users SET password_hash='' WHERE id=$1`},
	} {
		t.Run(change.name, func(t *testing.T) {
			pool, _ := fixture(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 2001,id,'one@test','One','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
 INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 2002,id,'two@test','Two','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;`); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			start := make(chan struct{})
			for _, id := range []int64{2001, 2002} {
				wg.Add(1)
				go func(id int64) {
					defer wg.Done()
					<-start
					_, err := pool.Exec(ctx, change.sql, id)
					results <- err
				}(id)
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				}
			}
			if successes != 1 {
				t.Fatalf("successes=%d, need exactly one", successes)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE is_active`); err == nil {
				t.Fatal("deleted last authenticatable Administrator")
			}
		})
	}
}

func TestCustomerMutationAuditFailureRollsBackAndRevocationRejects(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	admin := authorization.Actor{UserID: id}
	name := "Must not persist"
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_customer_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='customer' THEN RAISE EXCEPTION 'audit failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_customer_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_customer_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveCustomer(ctx, admin, 0, authorization.CustomerInput{DisplayName: &name}); err == nil {
		t.Fatal("audit failure accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial effect: count=%d err=%v", count, err)
	}
}

func TestRevocationCommittedWhileRequestWaitsRejectsWholeCustomerWrite(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin, user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Editor", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) SELECT id,'editor@test','Editor','hash','tech',$1 FROM companies LIMIT 1 RETURNING id`, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.create'`, role); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	name := "Revoked write"
	go func() {
		_, err := svc.SaveCustomer(requestCtx, authorization.Actor{UserID: user}, 0, authorization.CustomerInput{DisplayName: &name})
		done <- err
	}()
	for {
		var waiting bool
		if err := pool.QueryRow(requestCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("revoked write: %v", err)
	}
	var effects, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM customers),(SELECT count(*) FROM authorization_audit WHERE target_type='customer')`).Scan(&effects, &audits); err != nil || effects != 0 || audits != 0 {
		t.Fatalf("partial revoked action: effects=%d audits=%d err=%v", effects, audits, err)
	}
}

func TestCustomerGrantsRequireCompatibleViewAndSurviveRename(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: admin}
	p, err := svc.Principal(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if p.Allows("customer.create", authorization.Context) || p.Allows("customer.view", authorization.Own) || p.Allows("unknown", authorization.All) {
		t.Fatal("unsupported action/scope widened")
	}
	if _, err := svc.SaveRole(ctx, actor, 0, "Broken", []authorization.Grant{{Permission: "customer.edit", Scope: authorization.All}}); err == nil {
		t.Fatal("edit without View accepted")
	}
	role, err := svc.SaveRole(ctx, actor, 0, "Office", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, actor, role, "Renamed", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, actor, role, "Narrowed", []authorization.Grant{{Permission: "customer.view", Scope: authorization.Context}, {Permission: "customer.edit", Scope: authorization.All}}, 2); err == nil {
		t.Fatal("incompatible narrowing accepted")
	}
}

func TestProtectedCatalogAndFreshDefaults(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE authorization_roles SET name='Renamed' WHERE id=1`, `DELETE FROM authorization_grants WHERE role_id=1`, `DELETE FROM authorization_roles WHERE id=1`} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("protected identity/grants changed: %s", statement)
		}
	}
	var c, a string
	if err := pool.QueryRow(ctx, `SELECT scope FROM authorization_grants WHERE role_id=3 AND permission='customer.view'`).Scan(&c); err != nil || c != "C" {
		t.Fatalf("Technician default: %s %v", c, err)
	}
	if err := pool.QueryRow(ctx, `SELECT scope FROM authorization_grants WHERE role_id=2 AND permission='customer.edit'`).Scan(&a); err != nil || a != "A" {
		t.Fatalf("Dispatcher default: %s %v", a, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_permissions(permission,scope) VALUES('future.view','C'),('future.view','A')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants WHERE permission='future.view' AND role_id<>1`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new permission widened existing Roles: %d %v", count, err)
	}
	p, err := svc.Principal(ctx, authorization.Actor{UserID: admin})
	if err != nil || !p.Allows("future.view", authorization.All) {
		t.Fatalf("Administrator did not acquire new broadest permission: %v", err)
	}
}

func TestRoleReplacementIsAtomicAndStaleEditorCannotRestoreGrants(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin, user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, actor, 0, "Office", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) SELECT id,'subject@test','Subject','hash','tech',$1 FROM companies LIMIT 1 RETURNING id`, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, actor, role, "Empty", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, actor, role, "Stale", grants, 1); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("stale grant editor accepted: %v", err)
	}
	if err := svc.RemoveRole(ctx, actor, role, 999999); err == nil {
		t.Fatal("nonexistent replacement accepted")
	}
	p, err := svc.Principal(ctx, authorization.Actor{UserID: user})
	if err != nil || p.RoleID != role || len(p.Grants) != 0 {
		t.Fatalf("failed replacement or stale editor changed identity: %+v %v", p, err)
	}
	if err := svc.RemoveRole(ctx, actor, role, 4); err != nil {
		t.Fatal(err)
	}
	p, err = svc.Principal(ctx, authorization.Actor{UserID: user})
	if err != nil || p.RoleID != 4 {
		t.Fatalf("replacement did not preserve assigned user: %+v %v", p, err)
	}
}

func TestCustomerMutationPreservesLegacyRecordedFieldsWithoutCRMLifecycle(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	first, last, name := "Mary", "Jones", "Mary Jones"
	c, err := svc.SaveCustomer(ctx, authorization.Actor{UserID: admin}, 0, authorization.CustomerInput{FirstName: &first, LastName: &last, DisplayName: &name})
	if err != nil || c.DisplayName != name {
		t.Fatalf("explicit identity lost: %+v %v", c, err)
	}
	// A retained legacy CRM fact is neither rewritten nor an editable lifecycle.
	if _, err = pool.Exec(ctx, `UPDATE customers SET status='customer' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	notes := "Deliberate public service instructions"
	updated, err := svc.SaveCustomer(ctx, authorization.Actor{UserID: admin}, c.ID, authorization.CustomerInput{ServiceNotes: &notes})
	if err != nil || updated.DisplayName != "Mary Jones" || updated.Status != "customer" || updated.ServiceNotes != notes {
		t.Fatalf("unrelated fields changed during authorized patch: %+v %v", updated, err)
	}
}

func TestAdministratorMayDisableSelfOnlyWithAuthenticatableReplacement(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 3001,id,'first@test','First','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
 INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 3002,id,'second@test','Second','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;`); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEnabled(ctx, authorization.Actor{UserID: 3001}, 3001, false); err != nil {
		t.Fatalf("valid self-disable rejected: %v", err)
	}
	if err := svc.SetEnabled(ctx, authorization.Actor{UserID: 3002}, 3002, false); err == nil {
		t.Fatal("last Administrator disabled self")
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT is_active FROM users WHERE id=3002`).Scan(&active); err != nil || !active {
		t.Fatalf("last Administrator lost: %v %v", active, err)
	}
}

func TestSessionExpiryIsCheckedAgainstCommitTimeClock(t *testing.T) {
	pool, _ := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES($1,'expiry-fixture','web',clock_timestamp()+interval '1 hour')`, admin); err != nil {
		t.Fatal(err)
	}
	cfg := pool.Config().Copy()
	application := fmt.Sprintf("expiry_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["application_name"] = application
	requests, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer requests.Close()
	svc := authorization.New(requests)
	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(ctx)
	if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	name := "Expired request"
	go func() {
		_, err := svc.SaveCustomer(requestCtx, authorization.Actor{UserID: admin, SessionHash: "expiry-fixture"}, 0, authorization.CustomerInput{DisplayName: &name})
		done <- err
	}()
	var started time.Time
	for {
		err := pool.QueryRow(requestCtx, `SELECT xact_start FROM pg_stat_activity WHERE application_name=$1 AND $2::integer=ANY(pg_blocking_pids(pid))`, application, pid).Scan(&started)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The expiry is after transaction start but before it obtains the fence.
	// PostgreSQL now() is frozen at transaction start and must not authorize it.
	if _, err := fence.Exec(ctx, `UPDATE sessions SET expires_at=$1::timestamptz+interval '1 microsecond' WHERE token_hash='expiry-fixture'`, started); err != nil {
		t.Fatal(err)
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("expired session authorized mutation: %v", err)
	}
}

func TestOwnAndAssignedScopesCombineWithoutWideningToAllRecords(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin, user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Own and assigned", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.view", Scope: authorization.Assigned}, {Permission: "time.clock_in", Scope: authorization.Own}})
	if err != nil {
		t.Fatalf("valid independent scopes were rejected: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) SELECT id,'scopes@test','Scoped user','hash','tech',$1 FROM companies LIMIT 1 RETURNING id`, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Principal(ctx, authorization.Actor{UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Allows("time.view", authorization.Own) || !p.Allows("time.view", authorization.Assigned) || p.Allows("time.view", authorization.All) {
		t.Fatal("fixed scope union widened or lost authority")
	}
}
