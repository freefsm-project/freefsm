package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDiscoveryPopulatedUpgradePreservesHTTPHistory(t *testing.T) {
	ctx := context.Background()
	migrations := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "126_" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		migrations[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, migrations)
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Upgrade106 owner").SaveX(ctx)
	client.Item.Create().SetCompanyID(company).SetName("Upgrade106 item").SetSku("UPGRADE106").SetUnitPrice(123.45).SetUnitCost(987.65).SaveX(ctx)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	_, err = svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	role, err := svc.SaveRole(ctx, actor, 0, "Upgrade106 custom", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}})
	historyCheck(t, err)
	if role == 0 {
		t.Fatal("custom Role missing")
	}
	def := url.Values{"name": {"Upgrade106 field"}, "object_type": {"estimate"}, "field_type": {"text"}}
	fieldPost(t, web, cookie, "/settings/custom-fields", def, 303)
	var field int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE name='Upgrade106 field'`).Scan(&field))
	fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Upgrade106 estimate"}, "line_items": {"[]"}, fmt.Sprintf("custom_field_%d", field): {"Recorded106 value"}}, 303)
	var estimate int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Upgrade106 estimate'`).Scan(&estimate))
	source := fmt.Sprintf("/estimates/%d", estimate)
	fieldPost(t, web, cookie, source+"/comments", url.Values{"content": {"Preserved106 comment"}}, 200)
	conversionUpload(t, web, cookie, "estimate", estimate, "retained.txt", "Preserved106 bytes")
	def.Set("retired", "true")
	fieldPost(t, web, cookie, fmt.Sprintf("/settings/custom-fields/%d", field), def, 303)
	converted := fieldPost(t, web, cookie, source+"/convert", conversionReview(t, web, cookie, source+"/convert"), 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, web, cookie, invoice+"/revert-to-estimate", conversionReview(t, web, cookie, invoice+"/revert-to-estimate"), 303)
	fieldPost(t, web, cookie, source+"/delete", nil, 303)
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename<>'schema_migrations' ORDER BY tablename`)
	historyCheck(t, err)
	var tables []string
	for rows.Next() {
		var name string
		historyCheck(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	rows.Close()
	historyCheck(t, rows.Err())
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var data string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]')::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{table}.Sanitize()+` t) x`).Scan(&data))
			out[table] = data
		}
		var sequences string
		historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY sequencename),'[]')::text FROM pg_sequences s WHERE schemaname=current_schema()`).Scan(&sequences))
		out["sequences"] = sequences
		return out
	}
	before := snapshot()
	for range 2 {
		historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
		discoveryEqual(t, snapshot(), before)
	}
	// The upgraded production route can read the archived source but never
	// exposes the reverted Invoice tombstone.
	o := discoveryObserve(t, requestBody(t, web, cookie, "GET", "/search?q=Upgrade106&type=estimate&state=archived", 200))
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("estimate:%d", estimate)})
	discoveryEqual(t, discoveryObserve(t, requestBody(t, web, cookie, "GET", "/search?q=Upgrade106&type=invoice&state=all", 200)).Total, 0)
}
