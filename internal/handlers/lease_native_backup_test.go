package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLeaseNativeEncryptedBackupNormalRoles(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Native Lease source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Native Lease destination")
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	userIDs := map[string]int64{}
	for _, facet := range []string{"base", "billing"} {
		keys := []string{"lease.view", "lease.manage", "lease.archive", "lease.archived.view", "lease.restore", "customer.view", "location.view", "asset.view", "comment.add", "file.upload", "file.download", "tag.attach"}
		if facet == "billing" {
			keys = []string{"lease.billing.view", "lease.billing.manage", "lease.archived.view"}
		}
		grants := []authorization.Grant{}
		for _, key := range keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		svc := authorization.New(src.pool)
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: src.actor}, 0, "Native backup "+facet, grants)
		historyCheck(t, err)
		u := src.client.User.Create().SetCompanyID(src.company).SetName(facet + " Lease user").SetEmail(facet + "@native-lease.test").SetPasswordHash("known-test-password-hash").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: src.actor}, u.ID, role))
		userIDs[facet] = u.ID
	}
	baseCookie := sessionCookie(t, ctx, src.sessions, userIDs["base"])
	billingCookie := sessionCookie(t, ctx, src.sessions, userIDs["billing"])
	adminCookie := sessionCookie(t, ctx, src.sessions, src.actor)
	c := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Preserved Lease Customer").SaveX(ctx)
	l := src.client.Location.Create().SetCompanyID(src.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Preserved premises").SaveX(ctx)
	typ := src.client.AssetType.Create().SetCompanyID(src.company).SetName("Preserved equipment").SaveX(ctx)
	asset := src.client.Asset.Create().SetCompanyID(src.company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Preserved intent").SaveX(ctx)
	definition := src.client.CustomFieldDefinition.Create().SetCompanyID(src.company).SetObjectType("lease").SetName("Preserved typed date").SetFieldType("date").SaveX(ctx)
	_, err = src.pool.Exec(ctx, `UPDATE company_settings SET lease_prefix='BACK-',next_lease_number=91 WHERE company_id=$1`, src.company)
	historyCheck(t, err)
	fieldPost(t, web, baseCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, baseCookie, path, url.Values{"revision": {"0"}, "title": {"Preserved base-only title"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}, fmt.Sprintf("custom_field_%d", definition.ID): {"2024-02-29"}}, 303)
	fieldPost(t, web, billingCookie, path+"/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(asset.ID)}, "rent_cents": {"7234567"}, "initial_cents": {"1234"}, "tax_rate": {"7.123456789"}}, 303)
	fieldPost(t, web, baseCookie, path+"/comments", url.Values{"content": {"Preserved native discussion"}}, 200)
	tag := src.client.Tag.Create().SetCompanyID(src.company).SetName("Preserved native tag").SaveX(ctx)
	fieldPost(t, web, baseCookie, fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), nil, 200)
	upload := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	if response := uploadFileHTTP(t, upload, baseCookie, id, "preserved-agreement.txt", []byte("Exact externally supplied Lease bytes\n")); response.Code != 200 {
		t.Fatalf("upload %d %s", response.Code, response.Body.String())
	}
	var file int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&file))
	fieldPost(t, web, baseCookie, path, url.Values{"revision": {"1"}, "title": {"Preserved base-only title"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}, "agreement_file_id": {fmt.Sprint(file)}}, 303)
	fieldPost(t, web, baseCookie, path+"/delete", nil, 303)
	fieldPost(t, web, adminCookie, fmt.Sprintf("/locations/%d/delete", l.ID), nil, 303)
	fieldPost(t, web, adminCookie, fmt.Sprintf("/assets/%d/delete", asset.ID), nil, 303)
	_, err = src.pool.Exec(ctx, `SELECT set_lease_master_dependencies(900,$1,$2,NULL,'terminated',true,'[]')`, src.company, c.ID)
	historyCheck(t, err)
	var workflow int64
	tx, err := src.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, src.company).Scan(&workflow))
	for _, category := range []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"} {
		_, err = tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, "invoice:"+category)
		historyCheck(t, err)
	}
	historyCheck(t, tx.Commit(ctx))
	finalized, finalizeValues := populateFinalizedLease(t, src.pool, src.client, web, adminCookie, src.company)
	voidLeaseInvoiceAndReplay(t, src.pool, web, adminCookie, src.company, src.actor, finalized, finalizeValues)
	tables := []string{"leases", "lease_intended_assets", "lease_draft_billing", "lease_draft_asset_terms", "lease_master_dependencies", "lease_asset_dependencies", "lease_finalizations", "lease_billing_obligations", "lease_invoice_generations", "invoices", "issued_document_numbers", "company_settings", "customers", "locations", "assets", "asset_types", "comments", "files", "tags", "tag_links", "custom_field_definitions", "users", "authorization_roles", "authorization_grants"}
	snapshot := func(pool *pgxpool.Pool) map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var value string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&value))
			if table == "files" {
				value = strings.ReplaceAll(strings.ReplaceAll(value, src.uploadDir, "<upload-root>"), dst.uploadDir, "<upload-root>")
			}
			out[table] = value
		}
		return out
	}
	before := snapshot(src.pool)
	logLeaseInventory(t, "encrypted backup source inventory (root-relative File paths)", before)
	var through, lastID int64
	var audit, sequences string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&through, &audit))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('leases','id')::regclass)`).Scan(&lastID))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname='public' AND sequencename IN ('leases_id_seq','lease_billing_obligations_id_seq','invoices_id_seq')`).Scan(&sequences))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted native restore preserves all master, agreement, field, collaborator, grant and numbering facts", snapshot(dst.pool), before)
	logLeaseInventory(t, "encrypted backup destination inventory (root-relative File paths)", snapshot(dst.pool))
	t.Logf("distinct normal application owners: source=%s destination=%s", src.role, dst.role)
	var restoredAudit string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, through).Scan(&restoredAudit))
	historyEqual(t, "immutable native audit preserved", restoredAudit, audit)
	var restoredLast int64
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('leases','id')::regclass)`).Scan(&restoredLast))
	historyEqual(t, "native identity sequence preserved", restoredLast, lastID)
	var restoredSequences string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname='public' AND sequencename IN ('leases_id_seq','lease_billing_obligations_id_seq','invoices_id_seq')`).Scan(&restoredSequences))
	historyEqual(t, "Lease, obligation and Invoice sequence identities preserved", restoredSequences, sequences)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}))
	assertFinalizedPreservation(t, dst.pool, finalized)
	adminCookie = sessionCookie(t, ctx, dst.sessions, src.actor)
	fieldPost(t, web, adminCookie, fmt.Sprintf("/leases/%d/finalize", finalized), finalizeValues, 303)
	historyEqual(t, "restored Void replay preserves pending and generation IDs and numbering", snapshot(dst.pool), before)
	baseCookie = sessionCookie(t, ctx, dst.sessions, userIDs["base"])
	billingCookie = sessionCookie(t, ctx, dst.sessions, userIDs["billing"])
	body := requestBody(t, web, baseCookie, "GET", path, 200)
	assertContains(t, body, "Preserved base-only title")
	if strings.Contains(body, "7234567") {
		t.Fatal("restored base facet leaked Billing")
	}
	body = requestBody(t, web, billingCookie, "GET", path+"/billing", 200)
	assertContains(t, body, "7234567")
	if strings.Contains(body, "Preserved base-only title") {
		t.Fatal("restored Billing leaked base")
	}
	requestBody(t, web, billingCookie, "GET", path, 404)
	requestBody(t, web, baseCookie, "GET", path+"/billing", 404)
	if bytes := requestBody(t, web, baseCookie, "GET", fmt.Sprintf("/files/%d", file), 200); bytes != "Exact externally supplied Lease bytes\n" {
		t.Fatalf("restored file bytes changed: %q", bytes)
	}
	assertContains(t, requestBody(t, web, baseCookie, "GET", path+"/comments", 200), "Preserved native discussion")
	fieldPost(t, web, baseCookie, path+"/restore", nil, 303)
	var stateOnly bool
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM locations WHERE id=$1 AND deleted_at IS NOT NULL) AND EXISTS(SELECT 1 FROM assets WHERE id=$2 AND deleted_at IS NOT NULL AND customer_id IS NULL) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$3) AND NOT EXISTS(SELECT 1 FROM invoices WHERE lease_id=$3) AND NOT EXISTS(SELECT 1 FROM leases WHERE id=900)`, l.ID, asset.ID, id).Scan(&stateOnly))
	if !stateOnly {
		t.Fatal("restored native history revived dependencies or fabricated agreements/effects")
	}
	fieldPost(t, web, baseCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	assertContains(t, requestBody(t, web, baseCookie, "GET", "/leases", 200), "BACK-00093")
}
