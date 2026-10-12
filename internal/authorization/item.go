package authorization

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ItemInput is a facet patch. Absence preserves the stored value and conveys no
// request to edit another facet, including on forms with hidden controls.
type ItemInput struct {
	Name, Type, Description           *string
	Sku, TaxRate                      *string
	UnitPrice, UnitCost               *float64
	Taxable, TrackInventory, IsActive *bool
}

func (s *Service) SaveItem(ctx context.Context, a Actor, id int64, in ItemInput) (int64, error) {
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		creating := id == 0
		action := "edit"
		if creating {
			action = "create"
		}
		if !p.Allows("item.view", All) {
			return ErrNotFound
		}
		base := creating || in.Name != nil || in.Type != nil || in.Description != nil || in.TrackInventory != nil || in.IsActive != nil
		price := in.Sku != nil || in.UnitPrice != nil || in.Taxable != nil || in.TaxRate != nil
		if !base && !price && in.UnitCost == nil {
			return errors.New("no Item changes supplied")
		}
		if base && !p.Allows("item."+action, All) || price && !p.Allows("pricebook."+action, All) || in.UnitCost != nil && !p.Allows("cost.edit", All) {
			return ErrForbidden
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) == "" || in.Type != nil && *in.Type != "service" && *in.Type != "product" {
			return errors.New("Item requires name and Service or Product type")
		}
		for _, n := range []*float64{in.UnitPrice, in.UnitCost} {
			if n != nil && (*n < 0 || math.IsNaN(*n) || math.IsInf(*n, 0)) {
				return errors.New("Item amounts must be nonnegative and finite")
			}
		}
		if in.TaxRate != nil && *in.TaxRate != "" {
			rate, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(*in.TaxRate), "%")), 64)
			if err != nil || math.IsNaN(rate) || rate < 0 || rate > 100 {
				return errors.New("invalid tax rate")
			}
		}
		if creating {
			if in.Name == nil || in.Type == nil {
				return errors.New("Item requires name and type")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO items(company_id,name,type) VALUES($1,$2,$3) RETURNING id`, p.CompanyID, *in.Name, *in.Type).Scan(&id); err != nil {
				return err
			}
		} else {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE id=$1 AND (company_id=$2 OR company_id IS NULL) AND deleted_at IS NULL)`, id, p.CompanyID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		_, err := tx.Exec(ctx, `UPDATE items SET name=coalesce($2,name),type=coalesce($3,type),description=coalesce($4,description),sku=coalesce($5,sku),unit_price=coalesce($6,unit_price),unit_cost=coalesce($7,unit_cost),taxable=coalesce($8,taxable),tax_rate=coalesce($9,tax_rate),track_inventory=coalesce($10,track_inventory),is_active=coalesce($11,is_active),updated_at=clock_timestamp() WHERE id=$1`, id, in.Name, in.Type, in.Description, in.Sku, in.UnitPrice, in.UnitCost, in.Taxable, in.TaxRate, in.TrackInventory, in.IsActive)
		if err != nil {
			return err
		}
		for _, facet := range []struct {
			changed    bool
			permission string
		}{{base, "item." + action}, {price, "pricebook." + action}, {in.UnitCost != nil, "cost.edit"}} {
			if facet.changed {
				if err := audit(ctx, tx, a, p, facet.permission, All, "item", id, map[string]any{"facet": facet.permission}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return id, err
}

func (s *Service) SetItemArchived(ctx context.Context, a Actor, id int64, archived bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := "item.archive"
		if !archived {
			permission = "item.restore"
		}
		if a.Channel == "api" || !p.Allows(permission, All) {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE items SET deleted_at=CASE WHEN $3 THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp() WHERE id=$1 AND (company_id=$2 OR company_id IS NULL)`, id, p.CompanyID, archived)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit(ctx, tx, a, p, permission, All, "item", id, nil)
	})
}
