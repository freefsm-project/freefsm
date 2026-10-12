package authorization_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

func TestCatalogCorrectionPreservesFactsAndRequiresReviewedRepair(t *testing.T) {
	ctx := context.Background()
	pool := authorizationPool(t)
	db := &database.DB{Pool: pool}
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() > "055_authorization_expansion.up.sql" {
			continue
		}
		data, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	// These combinations were accepted by 055. Preserve the recorded decisions,
	// but never manufacture the missing views in an existing installation.
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 1001,id,'admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
 INSERT INTO authorization_roles(id,name) VALUES(50,'Recorded grants');
 INSERT INTO authorization_grants VALUES(50,'job.export','J'),(50,'time.view','O'),(50,'time.gps.export','O'),(50,'customer.view','A'),(50,'customer.restore','A');
 INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 1002,id,'subject@test','Subject','hash','dispatcher',50 FROM companies LIMIT 1;
 INSERT INTO authorization_reviews(user_id,role_id,user_revision,role_revision,approved_by) SELECT id,50,updated_at,1,1001 FROM users WHERE id=1002;
 INSERT INTO authorization_audit(actor_id,role_id,permission,scope,target_type,target_id,channel,detail) VALUES(1001,1,'role.edit','S','role',50,'web','{"recorded":"before correction"}');`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY permission,scope)::text FROM authorization_grants g WHERE role_id=50`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY permission,scope)::text FROM authorization_grants g WHERE role_id=50`).Scan(&after); err != nil || before != after {
		t.Fatalf("recorded grants changed: %v", err)
	}
	svc := authorization.New(pool)
	p, err := svc.Principal(ctx, authorization.Actor{UserID: 1002})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Allows("time.view", authorization.Own) || !p.Allows("customer.view", authorization.All) || p.Allows("job.export", authorization.Assigned) || p.Allows("time.gps.export", authorization.Own) || p.Allows("customer.restore", authorization.All) {
		t.Fatalf("invalid effective authority after correction: %+v", p.Grants)
	}
	admin := authorization.Actor{UserID: 1001}
	review, err := svc.Review(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range review.Roles {
		if role.ID == 50 {
			found = true
			if role.GrantError == "" || role.Revision != 2 {
				t.Fatalf("repair not exposed: %+v", role)
			}
		}
	}
	if !found {
		t.Fatal("recorded Role was lost")
	}
	for _, user := range review.Users {
		if user.ID == 1002 && user.Reviewed {
			t.Fatal("old approval silently survived changed catalog")
		}
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT actor_id=1001 AND role_id=1 AND action='role.edit' AND scope='S' AND actor_kind='product-user' AND detail->>'recorded'='before correction' FROM authorization_audit WHERE target_id=50`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("old immutable audit facts changed: %v", err)
	}
	// Restart/repeat migration cannot reseed defaults over the preserved grants.
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	restarted := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.archived.view", Scope: authorization.All}, {Permission: "customer.restore", Scope: authorization.All}}
	if _, err := restarted.SaveRole(ctx, admin, 50, "Explicit repair", grants, 2); err != nil {
		t.Fatal(err)
	}
	p, err = restarted.Principal(ctx, authorization.Actor{UserID: 1002})
	if err != nil || p.RoleID != 50 || !p.Allows("customer.restore", authorization.All) {
		t.Fatalf("explicit repair failed: %+v %v", p, err)
	}
}

func TestCutoverCannotApproveAnInvalidRecordedGrantSet(t *testing.T) {
	pool, svc := fixture(t, true)
	ctx := context.Background()
	actor := authorization.Actor{UserID: 1001}
	role, err := svc.SaveRole(ctx, actor, 0, "Previously accepted export", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
		t.Fatal(err)
	}
	review, err := svc.Review(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveUser(ctx, actor, 1002, role, review.Snapshot); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("invalid recorded grants approved: %v", err)
	}
}
