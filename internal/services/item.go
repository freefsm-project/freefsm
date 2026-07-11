package services

import (
	"context"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/item"
)

type ItemService struct {
	client *ent.Client
}

func NewItemService(client *ent.Client) *ItemService {
	return &ItemService{client: client}
}

func (s *ItemService) ListActive(ctx context.Context) ([]*ent.Item, error) {
	return s.client.Item.Query().Where(item.IsActive(true), item.DeletedAtIsNil()).Order(ent.Asc(item.FieldName)).All(ctx)
}

type ItemCreateParams struct {
	Name           string
	Type           string
	Sku            string
	UnitPrice      float64
	UnitCost       float64
	Taxable        bool
	TaxRate        string
	TrackInventory bool
	Description    string
	IsActive       bool
}

type ItemUpdateParams struct {
	Name           *string
	Type           *string
	Sku            *string
	UnitPrice      *float64
	UnitCost       *float64
	Taxable        *bool
	TaxRate        *string
	TrackInventory *bool
	Description    *string
	IsActive       *bool
}

func (s *ItemService) List(ctx context.Context, search string, page, perPage int) ([]*ent.Item, int, error) {
	q := s.client.Item.Query().Where(item.DeletedAtIsNil())

	if search != "" {
		q = q.Where(
			item.Or(
				item.NameContainsFold(search),
				item.SkuContainsFold(search),
				item.DescriptionContainsFold(search),
			),
		)
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count items: %w", err)
	}

	offset := PaginationOffset(page, perPage)
	items, err := q.
		Order(ent.Asc(item.FieldName)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list items: %w", err)
	}

	return items, total, nil
}

func (s *ItemService) GetByID(ctx context.Context, id int64) (*ent.Item, error) {
	i, err := s.client.Item.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get item %d: %w", id, err)
	}
	return i, nil
}

func (s *ItemService) Create(ctx context.Context, params ItemCreateParams) (*ent.Item, error) {
	i, err := s.client.Item.
		Create().
		SetName(params.Name).
		SetType(params.Type).
		SetSku(params.Sku).
		SetUnitPrice(params.UnitPrice).
		SetUnitCost(params.UnitCost).
		SetTaxable(params.Taxable).
		SetTaxRate(params.TaxRate).
		SetTrackInventory(params.TrackInventory).
		SetDescription(params.Description).
		SetIsActive(params.IsActive).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create item: %w", err)
	}
	return i, nil
}

func (s *ItemService) Update(ctx context.Context, id int64, params ItemUpdateParams) (*ent.Item, error) {
	u := s.client.Item.UpdateOneID(id)

	if params.Name != nil {
		u.SetName(*params.Name)
	}
	if params.Type != nil {
		u.SetType(*params.Type)
	}
	if params.Sku != nil {
		u.SetSku(*params.Sku)
	}
	if params.UnitPrice != nil {
		u.SetUnitPrice(*params.UnitPrice)
	}
	if params.UnitCost != nil {
		u.SetUnitCost(*params.UnitCost)
	}
	if params.Taxable != nil {
		u.SetTaxable(*params.Taxable)
	}
	if params.TaxRate != nil {
		u.SetTaxRate(*params.TaxRate)
	}
	if params.TrackInventory != nil {
		u.SetTrackInventory(*params.TrackInventory)
	}
	if params.Description != nil {
		u.SetDescription(*params.Description)
	}
	if params.IsActive != nil {
		u.SetIsActive(*params.IsActive)
	}

	i, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update item %d: %w", id, err)
	}
	return i, nil
}

func (s *ItemService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Item.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete item %d: %w", id, err)
	}
	return nil
}

func (s *ItemService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Item.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive item %d: %w", id, err)
	}
	return nil
}

func (s *ItemService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Item.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore item %d: %w", id, err)
	}
	return nil
}

func ItemPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

var ItemTypes = []string{"service", "product"}
