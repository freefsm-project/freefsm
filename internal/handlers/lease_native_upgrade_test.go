package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestLeasePopulatedNativeUpgradePreservesKnownFacts(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "109_" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Known native Customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Known premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Known equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Known intent").SaveX(ctx)
	definition := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Known field").SetFieldType("text").SaveX(ctx)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET lease_prefix='KNOWN-',next_lease_number=801 WHERE company_id=$1`, company)
	historyCheck(t, err)
	for range 2 {
		fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	}
	var ids []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM leases`).Scan(&ids))
	path := fmt.Sprintf("/leases/%d", ids[0])
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "title": {"Actual prior input"}, "reference": {"Known ref"}, "execution_date": {"2025-03-04"}, "premises_id": {fmt.Sprint(location.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}, fmt.Sprintf("custom_field_%d", definition.ID): {"Recorded prior value"}}, 303)
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"Recorded native comment"}}, 200)
	upload := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	if response := uploadFileHTTP(t, upload, cookie, ids[0], "known-native.txt", []byte("known native attachment")); response.Code != 200 {
		t.Fatalf("native file fixture: %d %s", response.Code, response.Body.String())
	}
	tag := client.Tag.Create().SetCompanyID(company).SetName("Recorded native tag").SaveX(ctx)
	fieldPost(t, web, cookie, fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), nil, 200)
	// These are explicit stored native-v108 facts, not values inferred by an upgrade.
	_, err = pool.Exec(ctx, `INSERT INTO lease_draft_billing(lease_id,revision,initial_due_on) VALUES($1,1,'2025-04-01');`, ids[0])
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO lease_draft_asset_terms VALUES($1,$2,12345,67890,7.123456789)`, ids[0], asset.ID)
	historyCheck(t, err)
	svc := authorization.New(pool)
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Existing limited Lease Role", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
	historyCheck(t, err)
	tx, err := pool.Begin(ctx)
	historyCheck(t, err)
	historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: ids[1], CustomerID: c.ID, Lifecycle: "draft", Archived: true}))
	_, err = tx.Exec(ctx, `UPDATE leases SET deleted_at='2025-05-06T10:00:00Z' WHERE id=$1`, ids[1])
	historyCheck(t, err)
	historyCheck(t, tx.Commit(ctx))
	_, err = pool.Exec(ctx, `SELECT set_lease_master_dependencies(900,$1,$2,NULL,'terminated',true,'[]')`, company, c.ID)
	historyCheck(t, err)
	tables := []string{"leases", "lease_intended_assets", "lease_draft_billing", "lease_draft_asset_terms", "lease_master_dependencies", "lease_asset_dependencies", "issued_document_numbers", "company_settings", "customers", "locations", "assets", "comments", "files", "tags", "tag_links", "custom_field_definitions", "authorization_roles", "authorization_grants", "authorization_audit", "activity_logs"}
	known := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			expression := "to_jsonb(r)"
			if table == "leases" {
				expression += "-'lifecycle_revision'"
			}
			var rows string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(`+expression+` ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows))
			out[table] = rows
		}
		return out
	}
	before := known()
	logLeaseInventory(t, "native pre-upgrade inventory", before)
	var sequences string
	var sequenceNames []string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text,array_agg(sequencename::text ORDER BY sequencename) FROM pg_sequences WHERE schemaname=current_schema()`).Scan(&sequences, &sequenceNames))
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "native upgrade preserves records, fields, grants, billing intent and audit", known(), before)
	logLeaseInventory(t, "native post-upgrade inventory", known())
	var afterSequences string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname=current_schema() AND sequencename::text=ANY($1::text[])`, sequenceNames).Scan(&afterSequences))
	historyEqual(t, "sequence states unchanged by upgrade", afterSequences, sequences)
	var unallocated bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_sequences WHERE schemaname=current_schema() AND NOT(sequencename::text=ANY($1::text[])) AND last_value IS NOT NULL)`, sequenceNames).Scan(&unallocated))
	if !unallocated {
		t.Fatal("upgrade allocated invented new identities")
	}
	var notInvented bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM leases WHERE id=900) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND NOT EXISTS(SELECT 1 FROM invoices) AND EXISTS(SELECT 1 FROM leases WHERE id=$1 AND execution_date IS NULL)`, ids[1]).Scan(&notInvented))
	if !notInvented {
		t.Fatal("upgrade manufactured agreement or historical effects")
	}
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases", 200), "KNOWN-00803")
}

func TestLeaseNativeProjectionReviewBeforeUpgrade(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "109_" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Explicit review Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Recorded legacy projection").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	svc := authorization.New(pool)
	in := authorization.LeaseDependencies{LeaseID: id, CustomerID: c.ID, Lifecycle: "draft", Coverage: []authorization.LeaseAssetDependency{{AssetID: asset.ID, StartsOn: "2025-02-03"}}}
	tx, err := pool.Begin(ctx)
	historyCheck(t, err)
	historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, in))
	historyCheck(t, tx.Commit(ctx))
	before := leaseDurableState(t, pool)
	err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS())
	if err == nil || !strings.Contains(err.Error(), "explicit mapping review") {
		t.Fatalf("ambiguous native projection did not stop recoverably: %v", err)
	}
	historyEqual(t, "failed preflight preserves original facts and immutable audit", leaseDurableState(t, pool), before)
	// Use the existing protected Product Administrator, not a fabricated migration
	// account or legacy Role label. Its explicit owning-hook repair captures the
	// old dated projection in immutable audit; migration does not infer the repair.
	p, err := svc.Administrator(ctx, authorization.Actor{UserID: admin})
	historyCheck(t, err)
	if !p.Protected {
		t.Fatal("review did not use protected Administrator")
	}
	in.Coverage = nil
	tx, err = pool.Begin(ctx)
	historyCheck(t, err)
	historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, in))
	historyCheck(t, tx.Commit(ctx))
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE action='lease.dependencies.change' AND target_id=$1 AND detail::text LIKE '%2025-02-03%') AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND EXISTS(SELECT 1 FROM leases WHERE id=$1 AND execution_date IS NULL)`, id).Scan(&retained))
	if !retained {
		t.Fatal("explicit review lost evidence or migration invented agreement facts")
	}
	// A caller cannot reintroduce the wrong model through the legacy hook.
	in.Coverage = []authorization.LeaseAssetDependency{{AssetID: asset.ID, StartsOn: "2025-02-03"}}
	tx, err = pool.Begin(ctx)
	historyCheck(t, err)
	historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, in))
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("native Draft acquired dated coverage")
	}
}

func TestLeaseRegistrationNeverAdoptsOpaqueLegacyReferences(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("New agreement Customer").SaveX(ctx)
	_, err := pool.Exec(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,'lease',1,$2,'Opaque pre-existing reference, not an agreement')`, company, admin)
	historyCheck(t, err)
	before := leaseDurableState(t, pool)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 409)
	historyEqual(t, "opaque reference was not adopted or overwritten by registration", leaseDurableState(t, pool), before)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	if id == 1 {
		t.Fatal("retained identity reused")
	}
	if body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/comments", id), 200); strings.Contains(body, "Opaque pre-existing") {
		t.Fatal("new agreement inherited unknown collaboration")
	}
}

func TestLeaseFinalizationReviewedPopulatedUpgrade(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "110_" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy coverage Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Legacy equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Retained historical coverage").SaveX(ctx)
	svc := authorization.New(pool)
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Retained view-only Lease Role", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
	historyCheck(t, err)
	write := func(id int64, start, end string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		historyCheck(t, err)
		defer tx.Rollback(ctx)
		historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: id, CustomerID: c.ID, Lifecycle: "terminated", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: start, EndsOn: &end}}}))
		historyCheck(t, tx.Commit(ctx))
	}
	write(900, "2020-01-01", "2020-02-01")
	write(901, "2020-02-01", "2020-03-01") // Inclusive shared day is incompatible.
	tables := []string{"leases", "lease_master_dependencies", "lease_asset_dependencies", "lease_draft_billing", "lease_draft_asset_terms", "authorization_roles", "authorization_grants", "authorization_audit", "issued_document_numbers", "company_settings", "invoices"}
	before := leasePreservationRows(t, pool, tables)
	err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS())
	if err == nil || !strings.Contains(err.Error(), "explicit mapping review") {
		t.Fatalf("overlapping legacy facts must stop recoverably: %v", err)
	}
	historyEqual(t, "failed migration preserves incompatible facts and authority", leasePreservationRows(t, pool, tables), before)
	// A protected human-authorized owning-hook correction records both versions;
	// migration neither guesses a boundary nor invents agreement terms/grants.
	write(901, "2020-02-02", "2020-03-01")
	before = leasePreservationRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	// invoices gains only the nullable origin column.
	historyEqual(t, "reviewed populated upgrade preserves all known facts", leasePreservationRows(t, pool, tables), before)
	var untouched bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM leases) AND NOT EXISTS(SELECT 1 FROM lease_finalizations) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations) AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations) AND EXISTS(SELECT 1 FROM authorization_audit WHERE target_id=901 AND detail::text LIKE '%2020-02-01%' AND detail::text LIKE '%2020-02-02%')`).Scan(&untouched))
	if !untouched {
		t.Fatal("upgrade invented native obligations or lost explicit review evidence")
	}
	id, values := populateFinalizedLease(t, pool, client, web, cookie, company)
	voidLeaseInvoiceAndReplay(t, pool, web, cookie, company, admin, id, values)
	tables = append(tables, "lease_finalizations", "lease_billing_obligations", "lease_invoice_generations")
	before = leasePreservationRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "migration replay preserves finalized coverage, pending IDs and Void provenance", leasePreservationRows(t, pool, tables), before)
	assertFinalizedPreservation(t, pool, id)
}
