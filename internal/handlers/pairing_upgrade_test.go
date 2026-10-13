package handlers

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/database"
)

func TestPairingUpgradePreservesInstalledAuthorityHTTP(t *testing.T) {
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() != "074_device_pairing.up.sql" {
			data, err := fs.ReadFile(source, entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[entry.Name()] = &fstest.MapFile{Data: data}
		}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	user := client.User.Create().SetCompanyID(company).SetName("Installed empty-role user").SetEmail("installed-pairing@test").SetRole("tech").SetPasswordHash("recorded-password").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	if _, err := sessions.CreateMobile(ctx, admin, "Installed device"); err != nil {
		t.Fatal(err)
	}
	client.Customer.Create().SetCompanyID(company).SetDisplayName("Recorded customer").SaveX(ctx)
	inventory := func() string {
		t.Helper()
		var raw string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),'roles',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_roles x),'grants',(SELECT jsonb_agg(to_jsonb(x) ORDER BY role_id,permission,scope) FROM authorization_grants x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM sessions x),'customers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customers x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x))::text`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := inventory()
	db := &database.DB{Pool: pool}
	if err := db.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if before != inventory() {
		t.Fatal("pairing migration rewrote installed accounts, grants, sessions or history")
	}
	if err := db.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if before != inventory() {
		t.Fatal("repeated migration changed installed data")
	}
	api := apiv1.NewRouter(pool, client, sessions)
	if w := pairingCall(api, "/pairings/redeem", approvedPairing(t, api, web, cookie)); w.Code != 201 {
		t.Fatalf("installed empty-role user pairing=%d", w.Code)
	}
}
