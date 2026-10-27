package handlers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMasterArchiveEncryptedBackupPreservesDependencies(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable owned cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Master archive source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Master archive destination")
	for _, workflow := range []struct {
		kind       string
		categories []string
	}{
		{"job", []string{"new", "travel_time", "in_progress", "pending", "completed", "canceled"}},
		{"project", []string{"new", "in_progress", "pending", "completed", "canceled"}},
		{"estimate", []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"}},
	} {
		tx, err := src.pool.Begin(ctx)
		historyCheck(t, err)
		var id int64
		historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,$2,$2) RETURNING id`, src.company, workflow.kind).Scan(&id))
		for _, category := range workflow.categories {
			_, err = tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, id, category, workflow.kind+":"+category)
			historyCheck(t, err)
		}
		historyCheck(t, tx.Commit(ctx))
	}
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	cookie := sessionCookie(t, ctx, src.sessions, src.actor)
	c := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Master backup owner").SaveX(ctx)
	contactPath := masterPost(t, web, cookie, fmt.Sprintf("/customers/%d/contacts", c.ID), url.Values{"first_name": {"Captured primary"}, "is_primary": {"true"}}, 303)
	locationPath := masterPost(t, web, cookie, fmt.Sprintf("/customers/%d/locations", c.ID), url.Values{"title": {"Captured primary site"}, "is_primary": {"true"}}, 303)
	var contact, location authorization.CustomerChild
	masterJSON(t, web, cookie, contactPath, &contact)
	masterJSON(t, web, cookie, locationPath, &location)
	typ := src.client.AssetType.Create().SetCompanyID(src.company).SetName("Backup equipment").SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Recorded inspection date"}, "object_type": {"asset"}, "field_type": {"date"}}, 303)
	var definition int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE object_type='asset' AND name='Recorded inspection date'`).Scan(&definition))
	fieldPost(t, web, cookie, "/assets", url.Values{"number": {"BACKUP-OWNED-63"}, "name": {"Customer-owned history"}, "ownership_class": {"customer_owned"}, "customer_id": {fmt.Sprint(c.ID)}, "location_id": {fmt.Sprint(location.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, fmt.Sprintf("custom_field_%d", definition): {"2024-02-29"}}, 303)
	owned := src.client.Asset.Query().OnlyX(ctx)
	var jobStatus, projectStatus int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed'`, src.company).Scan(&jobStatus))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed'`, src.company).Scan(&projectStatus))
	p := src.client.Project.Create().SetCompanyID(src.company).SetCustomerID(c.ID).SetLocationID(location.ID).SetStatusID(projectStatus).SetName("Finished historical Project").SaveX(ctx)
	j := src.client.Job.Create().SetCompanyID(src.company).SetCustomerID(c.ID).SetLocationID(location.ID).SetCustomerContactID(contact.ID).SetAssetID(owned.ID).SetProjectID(p.ID).SetStatusID(jobStatus).SetJobType("Finished historical Job").SaveX(ctx)
	jobPath := fmt.Sprintf("/jobs/%d", j.ID)
	i := src.client.Item.Create().SetCompanyID(src.company).SetName("Captured catalog identity").SetSku("BACKUP-SKU-63").SetUnitPrice(99).SaveX(ctx)
	fieldPost(t, web, cookie, jobPath, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Recorded price","quantity":2,"unit_price":11.25}]`, i.ID)}}, 303)
	fieldPost(t, web, cookie, jobPath+"/create-estimate", nil, 303)
	serviceEntryPost(t, web, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, owned.ID), url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Recorded service history"}, "responsible_user_id": {fmt.Sprint(src.actor)}}, 303)
	leaseSite := src.client.Location.Create().SetCompanyID(src.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Current Lease premises").SaveX(ctx)
	placed := src.client.Asset.Create().SetCompanyID(src.company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Current Company placement").SetNumber("BACKUP-PLACED-63").SaveX(ctx)
	returned := src.client.Asset.Create().SetCompanyID(src.company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Returned Company history").SetNumber("BACKUP-RETURNED-63").SaveX(ctx)
	for _, asset := range []int64{placed.ID, returned.ID} {
		fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/associations/place", asset), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "location_id": {fmt.Sprint(leaseSite.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Recorded placement"}, "revision": {"0"}}, 303)
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/associations/return", returned.ID), url.Values{"effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Recorded return"}, "revision": {"1"}}, 303)
	svc := authorization.New(src.pool)
	for _, dep := range []authorization.LeaseDependencies{
		{LeaseID: 63, CustomerID: c.ID, PremisesID: &leaseSite.ID, Lifecycle: "scheduled", Coverage: []authorization.LeaseAssetDependency{{AssetID: placed.ID, StartsOn: "2099-01-01"}}},
		{LeaseID: 64, CustomerID: c.ID, PremisesID: &location.ID, Lifecycle: "terminated", Coverage: []authorization.LeaseAssetDependency{{AssetID: returned.ID, StartsOn: "2099-01-01"}}},
	} {
		tx, err := src.pool.Begin(ctx)
		historyCheck(t, err)
		historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: src.actor}, dep))
		historyCheck(t, tx.Commit(ctx))
	}
	for _, path := range []string{contactPath, locationPath, fmt.Sprintf("/assets/%d", owned.ID), fmt.Sprintf("/assets/%d", returned.ID), fmt.Sprintf("/items/%d", i.ID)} {
		fieldPost(t, web, cookie, path+"/delete", nil, 303)
	}
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: src.actor}, 0, "Retained view without Restore", []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.archived.view", Scope: authorization.All}})
	historyCheck(t, err)
	tables := []string{"customers", "customer_contacts", "locations", "assets", "items", "jobs", "projects", "estimates", "job_contacts", "job_asset_links", "job_customer_blockers", "asset_association_origins", "asset_association_events", "asset_association_origin_revisions", "asset_service_entries", "asset_service_revisions", "asset_service_responsible_users", "custom_field_definitions", "lease_master_dependencies", "lease_asset_dependencies", "authorization_roles", "authorization_grants", "activity_logs"}
	snapshot := func(pool *pgxpool.Pool) map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var rows string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows))
			out[table] = rows
		}
		return out
	}
	before := snapshot(src.pool)
	var through int64
	var audit string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&through, &audit))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted restore preserves every master, dependency, capture, primary, grant and history fact", snapshot(dst.pool), before)
	var restoredAudit string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, through).Scan(&restoredAudit))
	historyEqual(t, "immutable master and registration audit survives", restoredAudit, audit)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}))
	cookie = sessionCookie(t, ctx, dst.sessions, src.actor)
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/restore", owned.ID), nil, 409)
	for _, path := range []string{contactPath, locationPath, fmt.Sprintf("/assets/%d", owned.ID), fmt.Sprintf("/assets/%d", returned.ID), fmt.Sprintf("/items/%d", i.ID)} {
		fieldPost(t, web, cookie, path+"/restore", nil, 303)
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/delete", placed.ID), nil, 409)
	fieldPost(t, web, cookie, fmt.Sprintf("/locations/%d/delete", leaseSite.ID), nil, 409)
	fieldPost(t, web, cookie, "/items", url.Values{"name": {"No SKU reuse"}, "type": {"service"}, "sku": {"BACKUP-SKU-63"}}, 409)
	after := snapshot(dst.pool)
	for _, table := range tables {
		if table == "customer_contacts" || table == "locations" || table == "assets" || table == "items" || table == "activity_logs" {
			continue
		}
		historyEqual(t, "explicit Restore does not rewrite "+table, after[table], before[table])
	}
	var primary bool
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT (SELECT is_primary FROM customer_contacts WHERE id=$1) OR (SELECT is_primary FROM locations WHERE id=$2)`, contact.ID, location.ID).Scan(&primary))
	if primary {
		t.Fatal("backup/Restore reinstated former primary")
	}
	if got := dst.client.Asset.GetX(ctx, owned.ID); got.CustomFields != owned.CustomFields || got.OwnershipClass != "customer_owned" || got.CustomerID != c.ID || got.LocationID == nil || *got.LocationID != location.ID {
		t.Fatalf("Restore rewrote owner/current Location/field metadata: %+v", got)
	}
}
