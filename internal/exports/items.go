package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// ItemOptions selects the complete Item result, never a page. Archived includes
// archived rows in addition to current rows. ArchivedOnly narrows either mode
// to archived rows alone. Either flag requires explicit archived authority.
type ItemOptions struct {
	Search       string `json:"search"`
	SearchSKU    bool   `json:"search_sku"`
	Sort         string `json:"sort"`
	Archived     bool   `json:"archived"`
	ArchivedOnly bool   `json:"archived_only"`
}

type ItemBase struct{}
type Pricebook struct{}
type Cost struct{}

type itemExportFacet string

const (
	itemBaseFacet      itemExportFacet = "item"
	itemPricebookFacet itemExportFacet = "pricebook"
	itemCostFacet      itemExportFacet = "cost"
)

func (ItemBase) Kind() string                { return "item-base-v1" }
func (Pricebook) Kind() string               { return "pricebook-v1" }
func (Cost) Kind() string                    { return "item-cost-v1" }
func (ItemBase) Permission() string          { return "item.export" }
func (Pricebook) Permission() string         { return "pricebook.export" }
func (Cost) Permission() string              { return "cost.export" }
func (ItemBase) Scope() authorization.Scope  { return authorization.All }
func (Pricebook) Scope() authorization.Scope { return authorization.All }
func (Cost) Scope() authorization.Scope      { return authorization.All }
func (ItemBase) Columns() []string           { return itemBaseFacet.columns() }
func (Pricebook) Columns() []string          { return itemPricebookFacet.columns() }
func (Cost) Columns() []string               { return itemCostFacet.columns() }

func (f itemExportFacet) columns() []string {
	columns := []string{"item_id", "name", "type", "description", "is_active", "archived"}
	switch f {
	case itemPricebookFacet:
		return append(columns, "sku", "unit_price", "taxable", "tax_rate")
	case itemCostFacet:
		return append(columns, "unit_cost")
	default:
		return columns
	}
}

func parseItemOptions(raw json.RawMessage) (ItemOptions, error) {
	var o ItemOptions
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&o) != nil {
		return o, ErrConflict
	}
	if d.Decode(new(any)) != io.EOF || len(o.Search) > 4096 {
		return o, ErrConflict
	}
	if o.Sort != "name" && o.Sort != "name_desc" && o.Sort != "updated" {
		return o, ErrConflict
	}
	return o, nil
}

func (ItemBase) Validate(raw json.RawMessage) error  { _, err := parseItemOptions(raw); return err }
func (Pricebook) Validate(raw json.RawMessage) error { return (ItemBase{}).Validate(raw) }
func (Cost) Validate(raw json.RawMessage) error      { return (ItemBase{}).Validate(raw) }

// Keep the complete compatible grant set here: Authorize runs even when a
// stored artifact contains only headers, and Recheck repeats it for every row.
func (f itemExportFacet) authorize(allows func(string, authorization.Scope) bool, archived, searchSKU bool) error {
	if !allows(string(f)+".export", authorization.All) || !allows("item.view", authorization.All) ||
		!allows(string(f)+".view", authorization.All) || archived && !allows("item.archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	if searchSKU && !allows("pricebook.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return nil
}

func (f itemExportFacet) authorizeOptions(allows func(string, authorization.Scope) bool, raw json.RawMessage) error {
	o, err := parseItemOptions(raw)
	if err != nil {
		return err
	}
	return f.authorize(allows, o.Archived || o.ArchivedOnly, o.SearchSKU)
}
func (ItemBase) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return itemBaseFacet.authorizeOptions(p.Allows, raw)
}
func (Pricebook) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return itemPricebookFacet.authorizeOptions(p.Allows, raw)
}
func (Cost) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return itemCostFacet.authorizeOptions(p.Allows, raw)
}

type itemExportReference struct {
	ID        int64           `json:"id"`
	Facet     itemExportFacet `json:"facet"`
	Archived  bool            `json:"archived"`
	SearchSKU bool            `json:"search_sku,omitempty"`
}

func (ItemBase) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return itemBaseFacet.snapshot(ctx, tx, p, raw, emit)
}
func (Pricebook) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return itemPricebookFacet.snapshot(ctx, tx, p, raw, emit)
}
func (Cost) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return itemCostFacet.snapshot(ctx, tx, p, raw, emit)
}

func (f itemExportFacet) snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	o, err := parseItemOptions(raw)
	if err != nil {
		return err
	}
	if err := f.authorize(p.Allows, o.Archived || o.ArchivedOnly, o.SearchSKU); err != nil {
		return err
	}
	order := "lower(name),id"
	if o.Sort == "name_desc" {
		order = "lower(name) DESC,id DESC"
	}
	if o.Sort == "updated" {
		order = "updated_at DESC,id DESC"
	}
	fields := "id,name,type,description,is_active::text,(deleted_at IS NOT NULL)::text"
	searchPredicate := "strpos(lower(name),lower($3))>0 OR strpos(lower(description),lower($3))>0"
	// pgx caches result descriptions by query text. Each FETCH must have a
	// facet-specific identity because base, Pricebook and Cost differ in width.
	// These identifiers are constants, never derived from request input.
	cursor := "item_base_export_snapshot"
	switch f {
	case itemPricebookFacet:
		cursor = "item_pricebook_export_snapshot"
		fields += ",sku,unit_price::text,taxable::text,tax_rate"
	case itemCostFacet:
		cursor = "item_cost_export_snapshot"
		fields += ",unit_cost::text"
	}
	if f == itemPricebookFacet || o.SearchSKU {
		searchPredicate += " OR strpos(lower(sku),lower($3))>0"
	}
	// SKU search is explicit and authorized for base/Cost; projections remain
	// facet-specific. A cursor holds one MVCC snapshot across bounded fetches.
	_, err = tx.Exec(ctx, `DECLARE `+cursor+` NO SCROLL CURSOR FOR SELECT `+fields+`
	 FROM items WHERE (company_id=$1 OR company_id IS NULL) AND ($2 OR deleted_at IS NULL)
	 AND (NOT $4::boolean OR deleted_at IS NOT NULL)
	 AND ($3='' OR `+searchPredicate+`) ORDER BY `+order,
		p.CompanyID, o.Archived || o.ArchivedOnly, o.Search, o.ArchivedOnly)
	if err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 128 FROM `+cursor)
		if err != nil {
			return err
		}
		batch := make([]Record, 0, 128)
		for rows.Next() {
			ref := itemExportReference{Facet: f, SearchSKU: o.SearchSKU}
			values := make([]string, len(f.columns()))
			dest := make([]any, len(values))
			dest[0] = &ref.ID
			for i := 1; i < len(values); i++ {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return err
			}
			values[0] = fmt.Sprint(ref.ID)
			ref.Archived = values[5] == "true"
			reference, err := json.Marshal(ref)
			if err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, Record{Values: values, Reference: reference})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if err := emit(row); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `CLOSE `+cursor)
	return err
}

func (ItemBase) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return itemBaseFacet.recheck(ctx, tx, p, raw)
}
func (Pricebook) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return itemPricebookFacet.recheck(ctx, tx, p, raw)
}
func (Cost) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return itemCostFacet.recheck(ctx, tx, p, raw)
}
func (f itemExportFacet) recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref itemExportReference
	if json.Unmarshal(raw, &ref) != nil || ref.ID <= 0 || ref.Facet != f {
		return authorization.ErrForbidden
	}
	if err := f.authorize(p.Allows, ref.Archived, ref.SearchSKU); err != nil {
		return err
	}
	var visible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE id=$1 AND (company_id=$2 OR company_id IS NULL) AND (deleted_at IS NULL OR $3))`, ref.ID, p.CompanyID, p.Allows("item.archived.view", authorization.All)).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return authorization.ErrForbidden
	}
	return nil
}

var (
	_ Definition = ItemBase{}
	_ Definition = Pricebook{}
	_ Definition = Cost{}
)
