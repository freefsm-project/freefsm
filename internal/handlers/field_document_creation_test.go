package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestFieldDocumentItemSnapshotHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	item := client.Item.Create().SetCompanyID(company).SetName("Field selection").SetType("service").SetUnitPrice(12.34).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Field catalog")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("GET", "/sync/items", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("snapshot = %d: %s", w.Code, w.Body.String())
	}
	serviceEntrySchemaValidator(t, api)("ItemSyncEnvelope", w.Body.Bytes())
	var page struct {
		Data struct {
			Records []struct {
				ID        string `json:"id"`
				LegacyID  int64  `json:"legacy_id"`
				UnitPrice string `json:"unit_price"`
			} `json:"records"`
			Complete bool   `json:"complete"`
			Cursor   string `json:"completion_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Data.Complete || page.Data.Cursor == "" || len(page.Data.Records) != 1 {
		t.Fatalf("incomplete catalog: %s", w.Body.String())
	}
	got := page.Data.Records[0]
	if got.ID == "" || got.LegacyID != item.ID || got.UnitPrice != "12.34" {
		t.Fatalf("lost installed identity or exact price: %+v", got)
	}
	client.Item.UpdateOneID(item.ID).SetUnitPrice(23.45).SaveX(ctx)
	delta := serviceAPIRequest(api, token, "GET", "/sync/items?cursor="+url.QueryEscape(page.Data.Cursor), nil)
	if delta.Code != 200 {
		t.Fatalf("delta = %d: %s", delta.Code, delta.Body.String())
	}
	serviceEntrySchemaValidator(t, api)("ItemSyncEnvelope", delta.Body.Bytes())
	if err := json.Unmarshal(delta.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Records) != 1 || page.Data.Records[0].ID != got.ID || page.Data.Records[0].UnitPrice != "23.45" {
		t.Fatalf("price drift replaced identity or disappeared: %s", delta.Body.String())
	}
	unchanged := serviceAPIRequest(api, token, "GET", "/sync/items?cursor="+url.QueryEscape(page.Data.Cursor), nil)
	if unchanged.Code != 200 {
		t.Fatalf("unchanged delta = %d: %s", unchanged.Code, unchanged.Body.String())
	}
	if err := json.Unmarshal(unchanged.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Records) != 0 {
		t.Fatalf("unchanged catalog manufactured changes: %s", unchanged.Body.String())
	}
	client.Item.UpdateOneID(item.ID).SetIsActive(false).SaveX(ctx)
	lost := serviceAPIRequest(api, token, "GET", "/sync/items?cursor="+url.QueryEscape(page.Data.Cursor), nil)
	if lost.Code != 409 {
		t.Fatalf("inactive selection retained = %d: %s", lost.Code, lost.Body.String())
	}
	serviceEntrySchemaValidator(t, api)("JobSyncResnapshotEnvelope", lost.Body.Bytes())
}

func TestFieldDocumentItemSnapshotFacetsAndPagingHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Catalog reader").SetEmail("catalog-reader@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Independent base", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
	for i := 0; i < 201; i++ {
		client.Item.Create().SetCompanyID(company).SetName(fmt.Sprintf("Item %03d", i)).SetType("product").SetSku(fmt.Sprintf("PRIVATE-SKU-%d", i)).SetUnitPrice(12.34).SetUnitCost(9.87).SaveX(ctx)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Base catalog")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	validate := serviceEntrySchemaValidator(t, api)
	path := "/sync/items?page_size=200"
	seen := map[string]bool{}
	var cursor string
	for {
		response := serviceAPIRequest(api, token, "GET", path, nil)
		if response.Code != 200 {
			t.Fatalf("catalog page = %d: %s", response.Code, response.Body.String())
		}
		validate("ItemSyncEnvelope", response.Body.Bytes())
		var page struct {
			Data struct {
				Records []map[string]any `json:"records"`
				Next    *string          `json:"next_page"`
				Cursor  *string          `json:"completion_cursor"`
			} `json:"data"`
		}
		historyCheck(t, json.Unmarshal(response.Body.Bytes(), &page))
		for _, item := range page.Data.Records {
			id := item["id"].(string)
			if seen[id] {
				t.Fatal("duplicate Item identity")
			}
			seen[id] = true
			for _, field := range []string{"sku", "unit_price", "taxable", "tax_rate", "unit_cost", "cost"} {
				if _, exists := item[field]; exists {
					t.Fatalf("unauthorized facet %s", field)
				}
			}
			if item["capabilities"].(map[string]any)["select"] != false {
				t.Fatal("base View granted selection")
			}
		}
		if page.Data.Next == nil {
			cursor = *page.Data.Cursor
			break
		}
		path = "/sync/items?page_size=200&page=" + url.QueryEscape(*page.Data.Next)
	}
	if len(seen) != 201 {
		t.Fatalf("catalog silently truncated to %d", len(seen))
	}
	for _, query := range []string{"scope=assigned", "page_size=201", "page_size=1&page_size=2", "search=hidden", "cursor=x&page=y"} {
		response := serviceAPIRequest(api, token, "GET", "/sync/items?"+query, nil)
		if response.Code != 400 {
			t.Fatalf("%s = %d", query, response.Code)
		}
		validate("ErrorEnvelope", response.Body.Bytes())
	}
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	_, err = auth.SaveRole(ctx, actor, role, "Independent base", nil, revision)
	historyCheck(t, err)
	denied := serviceAPIRequest(api, token, "GET", "/sync/items?page_size=200&cursor="+url.QueryEscape(cursor), nil)
	if denied.Code != 403 {
		t.Fatalf("revoked catalog = %d: %s", denied.Code, denied.Body.String())
	}
	validate("ErrorEnvelope", denied.Body.Bytes())
}
