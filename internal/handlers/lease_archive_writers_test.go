package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func leaseDurableState(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"leases", "lease_master_dependencies", "lease_asset_dependencies", "lease_intended_assets", "lease_draft_billing", "lease_draft_asset_terms", "issued_document_numbers", "comments", "files", "tag_links", "authorization_audit", "activity_logs"} {
		var rows string
		historyCheck(t, pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows))
		out[table] = rows
	}
	return out
}

func logLeaseInventory(t *testing.T, label string, rows map[string]string) {
	t.Helper()
	data, err := json.Marshal(rows)
	historyCheck(t, err)
	t.Logf("%s: %d tables, SHA256 %x", label, len(rows), sha256.Sum256(data))
}

func TestLeaseArchivedWriterInventoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive writer inventory").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended").SaveX(ctx)
	field := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Retained field").SetFieldType("text").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "asset_ids": {fmt.Sprint(asset.ID)}, fmt.Sprintf("custom_field_%d", field.ID): {"retained answer"}}, 303)
	fieldPost(t, web, cookie, path+"/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(asset.ID)}, "rent_cents": {"9876543"}}, 303)
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"Retained comment"}}, 200)
	var comment int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM comments`).Scan(&comment))
	tag := client.Tag.Create().SetCompanyID(company).SetName("Retained tag").SaveX(ctx)
	fieldPost(t, web, cookie, fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), nil, 200)
	upload := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	if response := uploadFileHTTP(t, upload, cookie, id, "retained.txt", []byte("retained bytes")); response.Code != 200 {
		t.Fatalf("upload %d: %s", response.Code, response.Body.String())
	}
	var file int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&file))
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	before := leaseDurableState(t, pool)
	commands := []struct {
		path   string
		form   url.Values
		status int
	}{
		{path, url.Values{"revision": {"2"}, "title": {"Rejected"}}, 403},
		{path + "/billing", url.Values{"revision": {"1"}, "asset_id": {fmt.Sprint(asset.ID)}, "rent_cents": {"1"}}, 403},
		{path + "/comments", url.Values{"content": {"Rejected"}}, 409},
		{fmt.Sprintf("%s/comments/%d/delete", path, comment), nil, 409},
		{fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), nil, 403},
		{fmt.Sprintf("%s/tags/%d/detach", path, tag.ID), nil, 403},
		{fmt.Sprintf("/files/%d/rename", file), url.Values{"revision": {"1"}, "original_name": {"changed.txt"}}, 404},
		{fmt.Sprintf("/files/%d/delete", file), url.Values{"revision": {"1"}}, 404},
		{path + "/custom-fields", url.Values{fmt.Sprintf("custom_field_%d", field.ID): {"Rejected"}}, 404},
	}
	for _, command := range commands {
		fieldPost(t, web, cookie, command.path, command.form, command.status)
	}
	if response := uploadFileHTTP(t, upload, cookie, id, "blocked.txt", []byte("blocked")); response.Code != 404 {
		t.Fatalf("archived upload: %d %s", response.Code, response.Body.String())
	}
	if _, err := authorization.New(pool).SaveFields(ctx, authorization.Actor{UserID: admin}, "lease", id, `[]`); err == nil {
		t.Fatal("field adapter bypassed Lease details/lifecycle")
	}
	historyEqual(t, "all denied ordinary writers preserve every effect and audit", leaseDurableState(t, pool), before)
	for _, query := range []string{
		`UPDATE leases SET title='SQL rejected' WHERE id=$1`,
		`UPDATE leases SET deleted_at=deleted_at-interval '1 day' WHERE id=$1`,
		`UPDATE lease_draft_asset_terms SET rent_cents=1 WHERE lease_id=$1`,
		`DELETE FROM lease_intended_assets WHERE lease_id=$1`,
		`UPDATE comments SET content='SQL rejected' WHERE object_type='lease' AND object_id=$1`,
		`UPDATE files SET original_name='SQL rejected.txt' WHERE object_type='lease' AND object_id=$1`,
		`UPDATE files SET file_path='arbitrary storage rewrite' WHERE object_type='lease' AND object_id=$1`,
		`DELETE FROM tag_links WHERE object_type='lease' AND object_id=$1`,
	} {
		if _, err := pool.Exec(ctx, query, id); err == nil {
			t.Fatalf("archived SQL mutation accepted: %s", query)
		}
	}
	historyEqual(t, "SQL guards preserve archived records and audit", leaseDurableState(t, pool), before)
	tx, err := pool.Begin(ctx)
	historyCheck(t, err)
	_, err = tx.Exec(ctx, `SELECT set_config('freefsm.backup_path_rebase','allowed',true)`)
	historyCheck(t, err)
	if _, err = tx.Exec(ctx, `UPDATE files SET original_name='not a path rebase.txt' WHERE id=$1`, file); err == nil {
		t.Fatal("backup path exception permitted ordinary File metadata editing")
	}
	historyCheck(t, tx.Rollback(ctx))
	fieldPost(t, web, cookie, fmt.Sprintf("/tags/%d/delete", tag.ID), nil, 409)
	fieldPost(t, web, cookie, fmt.Sprintf("/settings/custom-fields/%d/delete", field.ID), nil, 422)
	fieldPost(t, web, cookie, fmt.Sprintf("/settings/custom-fields/%d", field.ID), url.Values{"object_type": {"lease"}, "name": {"Retained field"}, "field_type": {"date"}}, 422)
	fieldPost(t, web, cookie, fmt.Sprintf("/settings/custom-fields/%d", field.ID), url.Values{"object_type": {"lease"}, "name": {"Retained field"}, "field_type": {"text"}, "retired": {"true"}}, 303)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "retained answer")
	assertContains(t, body, "Retired")
	for _, marker := range []string{"Save Draft details", "Upload files", "tag-select", "9876543"} {
		if strings.Contains(body, marker) {
			t.Fatalf("archived basic output exposed %s", marker)
		}
	}
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing", 200), "9876543")
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", file), 200), "retained bytes")
}

func TestLeaseLifecycleIndependentGrantsAndActiveCustomerHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent lifecycle").SaveX(ctx)
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	svc := authorization.New(pool)
	user := func(name string, keys ...string) *http.Cookie {
		t.Helper()
		g := []authorization.Grant{}
		for _, key := range keys {
			g = append(g, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, name, g)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName(name).SetEmail(name + "@lease65.test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		return sessionCookie(t, ctx, sessions, u.ID)
	}
	manager := user("manager", "lease.view", "lease.manage")
	archiver := user("archiver", "lease.view", "lease.archive")
	restorer := user("restorer", "lease.view", "lease.archived.view", "lease.restore")
	finance := user("finance-history", "lease.billing.view", "lease.billing.manage", "lease.archived.view")
	fieldPost(t, web, manager, path+"/delete", nil, 403)
	archivedResponse := fieldPost(t, web, archiver, path+"/delete", nil, 303)
	if archivedResponse.Header().Get("Location") != "/leases" {
		t.Fatal("Archive without archived View redirected into inaccessible history")
	}
	requestBody(t, web, manager, "GET", path, 404)
	requestBody(t, web, archiver, "GET", path, 404)
	requestBody(t, web, finance, "GET", path, 404)
	assertContains(t, requestBody(t, web, finance, "GET", path+"/billing", 200), "Archived")
	requestBody(t, web, finance, "GET", path+"/comments", 404)
	fieldPost(t, web, archiver, path+"/restore", nil, 403)
	var review authorization.CustomerArchivePreview
	masterJSON(t, web, adminCookie, fmt.Sprintf("/customers/%d/archive", c.ID), &review)
	fieldPost(t, web, adminCookie, fmt.Sprintf("/customers/%d/delete", c.ID), url.Values{"archive_snapshot": {review.Snapshot}}, 303)
	fieldPost(t, web, restorer, path+"/restore", nil, 409)
	if strings.Contains(requestBody(t, web, restorer, "GET", path, 200), ">Restore Draft<") {
		t.Fatal("ineligible restore offered")
	}
	fieldPost(t, web, adminCookie, fmt.Sprintf("/customers/%d/restore", c.ID), nil, 303)
	fieldPost(t, web, restorer, path+"/restore", nil, 303)
	body := requestBody(t, web, restorer, "GET", path, 200)
	if strings.Contains(body, "Save Draft details") {
		t.Fatal("Restore implied Manage")
	}
}
