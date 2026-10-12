package authorization

import (
	"context"

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
		rows, err := tx.Query(ctx, `SELECT id,name,type,description,sku,unit_price::text,taxable,tax_rate FROM items
 WHERE (company_id=$1 OR company_id IS NULL) AND is_active AND deleted_at IS NULL AND ($2::bigint=0 OR id=$2)
 AND ($3='' OR name ILIKE '%'||$3||'%' OR description ILIKE '%'||$3||'%' OR ($4 AND sku ILIKE '%'||$3||'%')) ORDER BY name,id LIMIT 200`, p.CompanyID, id, search, pricebook)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i ItemReference
			var sku, price, rate string
			var taxable bool
			if err := rows.Scan(&i.ID, &i.Name, &i.Type, &i.Description, &sku, &price, &taxable, &rate); err != nil {
				return err
			}
			if pricebook {
				i.Sku = &sku
				i.UnitPrice = &price
				i.Taxable = &taxable
				i.TaxRate = &rate
			}
			result = append(result, i)
		}
		if err := rows.Err(); err != nil {
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
