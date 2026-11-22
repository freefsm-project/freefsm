package exports

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestTicket97ItemGrantMatrix(t *testing.T) {
	permissions := []string{"item.view", "item.export", "pricebook.view", "pricebook.export", "cost.view", "cost.export", "item.archived.view", "item.edit"}
	for mask := 0; mask < 1<<len(permissions); mask++ {
		grants := map[string]bool{}
		for i, permission := range permissions {
			grants[permission] = mask&(1<<i) != 0
		}
		for _, facet := range []itemExportFacet{itemBaseFacet, itemPricebookFacet, itemCostFacet} {
			for _, archived := range []bool{false, true} {
				for _, archivedOnly := range []bool{false, true} {
					for _, searchSKU := range []bool{false, true} {
						want := grants["item.view"] && (!(archived || archivedOnly) || grants["item.archived.view"]) && (!searchSKU || grants["pricebook.view"])
						switch facet {
						case itemBaseFacet:
							want = want && grants["item.export"]
						case itemPricebookFacet:
							want = want && grants["pricebook.view"] && grants["pricebook.export"]
						case itemCostFacet:
							want = want && grants["cost.view"] && grants["cost.export"]
						}
						allows := func(permission string, scope authorization.Scope) bool {
							if scope != authorization.All {
								t.Fatalf("unexpected scope %s", scope)
							}
							return grants[permission]
						}
						raw, err := json.Marshal(ItemOptions{Sort: "name", Archived: archived, ArchivedOnly: archivedOnly, SearchSKU: searchSKU})
						if err != nil {
							t.Fatal(err)
						}
						err = facet.authorizeOptions(allows, raw)
						if (err == nil) != want {
							t.Fatalf("facet=%s options=%s mask=%d: %v", facet, raw, mask, err)
						}
						if err != nil && !errors.Is(err, authorization.ErrForbidden) {
							t.Fatal(err)
						}
						// Row rechecks use the same predicate with retained reference flags.
						if err := facet.authorize(allows, archived || archivedOnly, searchSKU); (err == nil) != want {
							t.Fatalf("reference authority facet=%s options=%s mask=%d: %v", facet, raw, mask, err)
						}
					}
				}
			}
		}
	}
}

func TestTicket97ItemSearchSKUReferences(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		ref := itemExportReference{ID: 1, Facet: itemBaseFacet, SearchSKU: enabled}
		raw, err := json.Marshal(ref)
		if err != nil {
			t.Fatal(err)
		}
		var restored itemExportReference
		if err := json.Unmarshal(raw, &restored); err != nil || restored != ref {
			t.Fatalf("reference round trip: %s %+v %v", raw, restored, err)
		}
		if !enabled && strings.Contains(string(raw), "search_sku") {
			t.Fatalf("optional flag not omitted: %s", raw)
		}
	}
	var legacy itemExportReference
	if err := json.Unmarshal([]byte(`{"id":1,"facet":"cost","archived":false}`), &legacy); err != nil || legacy.SearchSKU {
		t.Fatalf("legacy reference: %+v %v", legacy, err)
	}
	for _, raw := range []string{`{"sort":"name","search_sku":"true"}`, `{"sort":"name","search_sku":1}`} {
		if _, err := parseItemOptions(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid SKU flag accepted: %s", raw)
		}
	}
}

func TestTicket97ItemFixedColumns(t *testing.T) {
	cases := []struct {
		d    Definition
		want []string
	}{
		{ItemBase{}, []string{"item_id", "name", "type", "description", "is_active", "archived"}},
		{Pricebook{}, []string{"item_id", "name", "type", "description", "is_active", "archived", "sku", "unit_price", "taxable", "tax_rate"}},
		{Cost{}, []string{"item_id", "name", "type", "description", "is_active", "archived", "unit_cost"}},
	}
	for _, tc := range cases {
		if !reflect.DeepEqual(tc.d.Columns(), tc.want) {
			t.Fatalf("%s columns: %v", tc.d.Kind(), tc.d.Columns())
		}
		// The lifecycle calls this for empty exports as well as populated ones.
		if err := tc.d.Authorize(authorization.Principal{}, json.RawMessage(`{"sort":"name"}`)); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("unauthorized empty result: %v", err)
		}
	}
}

func TestTicket97ItemOptions(t *testing.T) {
	for _, d := range []Definition{ItemBase{}, Pricebook{}, Cost{}} {
		for _, raw := range []string{`{"sort":"name"}`, `{"sort":"name_desc","archived":true}`, `{"sort":"updated","search":"same name"}`} {
			if err := d.Validate(json.RawMessage(raw)); err != nil {
				t.Fatalf("%s valid %s: %v", d.Kind(), raw, err)
			}
		}
		for _, raw := range []string{`null`, ``, `{}`, `{"sort":"sku"}`, `{"sort":"name","page":2}`, `{"sort":"name"} {}`, `{"sort":"name","archived":"true"}`, `{"sort":"name","search":"` + strings.Repeat("x", 4097) + `"}`} {
			if err := d.Validate(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
				t.Fatalf("%s invalid options accepted: %s", d.Kind(), raw)
			}
		}
	}
}

func TestTicket97ItemArchivedOnlyOptionsAuthority(t *testing.T) {
	for _, facet := range []itemExportFacet{itemBaseFacet, itemPricebookFacet, itemCostFacet} {
		for _, archived := range []bool{false, true} {
			for _, only := range []bool{false, true} {
				raw, err := json.Marshal(ItemOptions{Sort: "name", Archived: archived, ArchivedOnly: only})
				if err != nil {
					t.Fatal(err)
				}
				o, err := parseItemOptions(raw)
				if err != nil || o.Archived != archived || o.ArchivedOnly != only {
					t.Fatalf("options %s: %+v, %v", raw, o, err)
				}
				for _, archiveGrant := range []bool{false, true} {
					err := facet.authorizeOptions(func(permission string, scope authorization.Scope) bool {
						return scope == authorization.All && (permission != "item.archived.view" || archiveGrant)
					}, raw)
					want := archiveGrant || (!archived && !only)
					if (err == nil) != want {
						t.Fatalf("facet=%s archived=%t only=%t grant=%t: %v", facet, archived, only, archiveGrant, err)
					}
				}
			}
		}
	}
	for _, raw := range []string{`{"sort":"name","archived_only":"true"}`, `{"sort":"name","archived_only":1}`} {
		if _, err := parseItemOptions(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid options accepted: %s", raw)
		}
	}
	// Older stored requests omit the additive flag and retain include semantics.
	o, err := parseItemOptions(json.RawMessage(`{"sort":"name","archived":true}`))
	if err != nil || !o.Archived || o.ArchivedOnly {
		t.Fatalf("legacy options: %+v, %v", o, err)
	}
}

func TestTicket97ItemInvalidReferencesFailClosed(t *testing.T) {
	for _, d := range []Definition{ItemBase{}, Pricebook{}, Cost{}} {
		for _, raw := range []string{`null`, `{}`, `{"id":-1}`, `{"id":1,"facet":"other"}`, `{"id":1,"facet":"item","archived":true}`} {
			if err := d.Recheck(context.Background(), nil, authorization.Principal{}, json.RawMessage(raw)); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("%s reference %s: %v", d.Kind(), raw, err)
			}
		}
	}
}
