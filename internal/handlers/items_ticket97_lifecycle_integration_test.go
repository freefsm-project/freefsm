package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Each subtest uses permissionHTTPFixture's migrated, uniquely named schema;
// these tests neither reuse nor modify another test's records or grants.
func TestTicket97ItemSKUSearchDurableAuthority(t *testing.T) {
	for _, definition := range []exports.Definition{exports.ItemBase{}, exports.Pricebook{}, exports.Cost{}} {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/populated=%t", definition.Kind(), populated), func(t *testing.T) {
				pool, client, _, _, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				auth := authorization.New(pool)
				grants := []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}, {Permission: definition.Permission(), Scope: authorization.All}}
				if definition.Permission() == "cost.export" {
					grants = append(grants, authorization.Grant{Permission: "cost.view", Scope: authorization.All})
				}
				role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "SKU export authority", grants)
				if err != nil {
					t.Fatal(err)
				}
				user := client.User.Create().SetCompanyID(company).SetEmail("item-sku-export@test").SetName("SKU exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
				if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
					t.Fatal(err)
				}
				actor := authorization.Actor{UserID: user.ID, Channel: "web"}
				if populated {
					if _, err := pool.Exec(ctx, `INSERT INTO items(company_id,name,sku,unit_price,unit_cost) VALUES($1,'Ordinary duplicate name','SKU-ONLY-NEEDLE',12.34,56.78)`, company); err != nil {
						t.Fatal(err)
					}
				}
				service := exports.New(pool, definition)
				options := json.RawMessage(`{"sort":"name","search":"SKU-ONLY-NEEDLE","search_sku":true}`)
				removePricebook := func() {
					t.Helper()
					if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='pricebook.view'`, role); err != nil {
						t.Fatal(err)
					}
				}
				removePricebook()
				if _, err := service.Accept(ctx, actor, uuid.NewString(), definition.Kind(), options); !errors.Is(err, authorization.ErrForbidden) {
					t.Fatalf("missing SKU authority accepted: %v", err)
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("denial persisted job: %d %v", count, err)
				}
				// Exercise Snapshot itself under a genuine database-loaded Principal.
				err = auth.WithinActor(ctx, actor, func(tx pgx.Tx, p authorization.Principal) error {
					return definition.Snapshot(ctx, tx, p, options, func(exports.Record) error { t.Fatal("denied snapshot emitted a row"); return nil })
				})
				if !errors.Is(err, authorization.ErrForbidden) {
					t.Fatalf("snapshot missing SKU authority: %v", err)
				}
				// Without opt-in Base and Cost retain independent access and do not
				// match a SKU-only term, even when the underlying Item exists.
				if definition.Permission() != "pricebook.export" {
					id := ticket97ReadyItemExport(t, service, actor, definition.Kind(), json.RawMessage(`{"sort":"name","search":"SKU-ONLY-NEEDLE"}`))
					if rows := ticket97ItemCSV(t, service, actor, id); len(rows) != 1 {
						t.Fatalf("default search leaked SKU matches: %v", rows)
					}
				}
				if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'pricebook.view','A')`, role); err != nil {
					t.Fatal(err)
				}
				id := ticket97ReadyItemExport(t, service, actor, definition.Kind(), options)
				rows := ticket97ItemCSV(t, service, actor, id)
				wantRows := 1
				if populated {
					wantRows++
				}
				if len(rows) != wantRows {
					t.Fatalf("SKU-only matching: %v", rows)
				}
				removePricebook()
				if populated {
					var ref json.RawMessage
					if err := pool.QueryRow(ctx, `SELECT reference FROM export_records WHERE export_id=$1 AND ordinal=1`, id).Scan(&ref); err != nil {
						t.Fatal(err)
					}
					var flags struct {
						SearchSKU bool `json:"search_sku"`
					}
					if err := json.Unmarshal(ref, &flags); err != nil || !flags.SearchSKU {
						t.Fatalf("missing retained SKU authority: %s %v", ref, err)
					}
					err := auth.WithinActor(ctx, actor, func(tx pgx.Tx, p authorization.Principal) error { return definition.Recheck(ctx, tx, p, ref) })
					if !errors.Is(err, authorization.ErrForbidden) {
						t.Fatalf("row recheck ignored revoked SKU authority: %v", err)
					}
				}
				download, err := service.PrepareDownload(ctx, actor, id)
				if download != nil {
					download.Close()
					t.Fatal("revoked artifact exposed download")
				}
				if !errors.Is(err, exports.ErrUnavailable) {
					t.Fatalf("revoked download: %v", err)
				}
				status, err := service.Status(ctx, actor, id)
				if err != nil || !status.Blocked || status.RowCount != nil || status.AsOf != nil {
					t.Fatalf("revocation status: %+v %v", status, err)
				}
				// The rejected transfer must not add a second download audit.
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='downloaded'`, id).Scan(&count); err != nil || count != 1 {
					t.Fatalf("download audit after denial: %d %v", count, err)
				}
			})
		}
	}
}

func TestTicket97ItemExactNumericDurableFacets(t *testing.T) {
	pool, _, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	actor := authorization.Actor{UserID: admin, Channel: "web"}
	amounts := [][2]string{{"0.00", "9999999999.99"}, {"0.01", "0.10"}, {"9999999999.99", "0.00"}, {"1234567890.12", "9876543210.98"}}
	ids := []string{}
	for i, pair := range amounts {
		var id int64
		// String parameters go directly to NUMERIC(12,2); no float seeds or
		// float-based expectations can hide conversion/rounding errors.
		if err := pool.QueryRow(ctx, `INSERT INTO items(company_id,name,sku,unit_price,unit_cost,tax_rate) VALUES($1,'Duplicate decimal name',$2,$3::numeric,$4::numeric,'7.125%') RETURNING id`, company, fmt.Sprintf("EXACT-%d", i), pair[0], pair[1]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	service := exports.New(pool, exports.ItemBase{}, exports.Pricebook{}, exports.Cost{})
	for _, definition := range []exports.Definition{exports.ItemBase{}, exports.Pricebook{}, exports.Cost{}} {
		id := ticket97ReadyItemExport(t, service, actor, definition.Kind(), json.RawMessage(`{"sort":"name"}`))
		rows := ticket97ItemCSV(t, service, actor, id)
		if len(rows) != len(amounts)+1 {
			t.Fatalf("%s row count: %v", definition.Kind(), rows)
		}
		if !reflect.DeepEqual(rows[0], definition.Columns()) {
			t.Fatalf("headers: %v", rows[0])
		}
		columns := map[string]int{}
		for i, name := range rows[0] {
			columns[name] = i
		}
		for i, row := range rows[1:] {
			if row[columns["item_id"]] != ids[i] {
				t.Fatalf("facet identity/order drift: %s %v", definition.Kind(), row)
			}
			switch definition.Permission() {
			case "pricebook.export":
				if row[columns["unit_price"]] != amounts[i][0] || row[columns["tax_rate"]] != "7.125%" || row[columns["sku"]] != fmt.Sprintf("EXACT-%d", i) {
					t.Fatalf("inexact Pricebook: %v", row)
				}
			case "cost.export":
				if row[columns["unit_cost"]] != amounts[i][1] {
					t.Fatalf("inexact Cost: %v", row)
				}
			}
		}
		if definition.Permission() != "cost.export" {
			if _, exists := columns["unit_cost"]; exists {
				t.Fatal("Cost leaked into other facet")
			}
		}
		if definition.Permission() != "pricebook.export" {
			for _, forbidden := range []string{"sku", "unit_price", "taxable", "tax_rate"} {
				if _, exists := columns[forbidden]; exists {
					t.Fatalf("%s leaked %s", definition.Kind(), forbidden)
				}
			}
		}
	}
}

func TestTicket97ItemSearchMatchesCollectionFields(t *testing.T) {
	pool, _, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	actor := authorization.Actor{UserID: admin, Channel: "web"}
	var itemID int64
	if err := pool.QueryRow(ctx, `INSERT INTO items(company_id,name,type,description,sku) VALUES($1,'Needle Name','product','Retained Detail','StockMarker') RETURNING id`, company).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	service := exports.New(pool, exports.ItemBase{}, exports.Pricebook{}, exports.Cost{})
	for _, definition := range []exports.Definition{exports.ItemBase{}, exports.Pricebook{}, exports.Cost{}} {
		for _, searchSKU := range []bool{false, true} {
			for _, tc := range []struct {
				search string
				match  bool
			}{
				{strconv.FormatInt(itemID, 10), false},
				{"product", false},
				{"Name Retained", false}, // Not a match spanning two fields.
				{"nEeDlE", true},
				{"dEtAiL", true},
				{"stockmarker", searchSKU || definition.Permission() == "pricebook.export"},
			} {
				options, err := json.Marshal(exports.ItemOptions{Sort: "name", Search: tc.search, SearchSKU: searchSKU})
				if err != nil {
					t.Fatal(err)
				}
				id := ticket97ReadyItemExport(t, service, actor, definition.Kind(), options)
				rows := ticket97ItemCSV(t, service, actor, id)
				want := 1
				if tc.match {
					want++
				}
				if len(rows) != want {
					t.Fatalf("%s search=%q search_sku=%t: %v", definition.Kind(), tc.search, searchSKU, rows)
				}
				if tc.match && rows[1][0] != strconv.FormatInt(itemID, 10) {
					t.Fatalf("wrong matching identity: %v", rows)
				}
			}
		}
	}
}

func ticket97ReadyItemExport(t *testing.T, service *exports.Service, actor authorization.Actor, kind string, options json.RawMessage) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := service.Accept(ctx, actor, uuid.NewString(), kind, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx, actor, id)
	if err != nil || status.State != "Ready" || status.Blocked {
		t.Fatalf("export not Ready: %+v %v", status, err)
	}
	return id
}

func ticket97ItemCSV(t *testing.T, service *exports.Service, actor authorization.Actor, id int64) [][]string {
	t.Helper()
	download, err := service.PrepareDownload(context.Background(), actor, id)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Close()
	rows, err := csv.NewReader(download).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
