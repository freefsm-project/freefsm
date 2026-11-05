package authorization

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// ItemReference is the public field projection: the field surface supports
// reusable pricing, never sensitive Cost or maintenance. Exact money is a string.
type ItemReference struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Sku         *string `json:"sku,omitempty"`
	UnitPrice   *string `json:"unit_price,omitempty"`
	Taxable     *bool   `json:"taxable,omitempty"`
	TaxRate     *string `json:"tax_rate,omitempty"`
}

func (s *Service) ItemReferences(ctx context.Context, a Actor, search string, id int64, selection bool) ([]ItemReference, error) {
	result := []ItemReference{}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("item.view", All) {
			return ErrNotFound
		}
		pricebook := p.Allows("pricebook.view", All)
		var err error
		result, err = readItemReferences(ctx, tx, p, search, id, 200)
		if err != nil {
			return err
		}
		if id > 0 && len(result) == 0 {
			return ErrNotFound
		}
		if selection && !pricebook {
			return ErrForbidden
		}
		return nil
	})
	return result, err
}

func readItemReferences(ctx context.Context, tx pgx.Tx, p Principal, search string, id int64, limit int) ([]ItemReference, error) {
	result := []ItemReference{}
	pricebook := p.Allows("pricebook.view", All)
	rows, err := tx.Query(ctx, `SELECT id,name,type,description,sku,unit_price::text,taxable,tax_rate FROM items
 WHERE (company_id=$1 OR company_id IS NULL) AND is_active AND deleted_at IS NULL AND ($2::bigint=0 OR id=$2)
 AND ($3='' OR name ILIKE '%'||$3||'%' OR description ILIKE '%'||$3||'%' OR ($4 AND sku ILIKE '%'||$3||'%')) ORDER BY name,id LIMIT NULLIF($5,0)`, p.CompanyID, id, search, pricebook, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i ItemReference
		var sku, price, rate string
		var taxable bool
		if err := rows.Scan(&i.ID, &i.Name, &i.Type, &i.Description, &sku, &price, &taxable, &rate); err != nil {
			return nil, err
		}
		if pricebook {
			i.Sku = &sku
			i.UnitPrice = &price
			i.Taxable = &taxable
			i.TaxRate = &rate
		}
		result = append(result, i)
	}
	return result, rows.Err()
}

// SyncItems shares the durable synchronization protocol, with a separate
// namespace and Item authority rather than an expansion of Assigned Job Context.
func (s *Service) SyncItems(ctx context.Context, a Actor, in SyncRequest) (SyncPage, error) {
	if in.PageSize < 1 || in.PageSize > 200 || in.Scope != "all" || a.SessionHash == "" {
		return SyncPage{}, ErrSyncResnapshot
	}
	in.Scope = "items:all"
	return s.syncRecords(ctx, a, in, "item.view", All, syncItemReadModel)
}

func syncItemReadModel(ctx context.Context, tx pgx.Tx, p Principal, _ Actor, _ string) ([]syncEntry, error) {
	items, err := readItemReferences(ctx, tx, p, "", 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]syncEntry, 0, len(items))
	for _, item := range items {
		id, err := syncIdentity(ctx, tx, "item", item.ID)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, err
		}
		data["id"], data["legacy_id"] = id, item.ID
		data["capabilities"] = map[string]bool{"select": p.Allows("pricebook.view", All)}
		out = append(out, syncEntry{InstalledID: item.ID, ID: id, Anchor: id, Data: data})
	}
	return out, nil
}
