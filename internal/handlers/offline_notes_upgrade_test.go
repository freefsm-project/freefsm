package handlers

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/database"
)

func TestOfflineNotesInstalledUpgrade(t *testing.T) {
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() == "086_offline_authorization.up.sql" {
			continue
		}
		b, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, _, _, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed offline owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed unchanged Job").SetTechNotes("Recorded original notes").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	inventory := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),'grants',(SELECT jsonb_agg(to_jsonb(x) ORDER BY role_id,permission,scope) FROM authorization_grants x),'customers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customers x),'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM jobs x),'assignments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job_assignments x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x),'admissions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM operation_admissions x),'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY opaque_id) FROM job_sync_identities x))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := inventory()
	for i := 0; i < 2; i++ {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
		if inventory() != before {
			t.Fatal("offline migration rewrote installed history or grants")
		}
	}
	var hours, count int
	if err := pool.QueryRow(ctx, `SELECT offline_hours,(SELECT count(*) FROM offline_authorization_receipts) FROM company_settings WHERE company_id=$1`, company).Scan(&hours, &count); err != nil {
		t.Fatal(err)
	}
	if hours != 24 || count != 0 {
		t.Fatalf("wrong policy or invented receipts: %d %d", hours, count)
	}
}
