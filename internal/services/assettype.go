package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/assettype"
)

type AssetTypeService struct {
	client *ent.Client
}

func NewAssetTypeService(client *ent.Client) *AssetTypeService {
	return &AssetTypeService{client: client}
}

func (s *AssetTypeService) List(ctx context.Context) ([]*ent.AssetType, error) {
	return s.client.AssetType.Query().
		Order(ent.Asc(assettype.FieldSortOrder)).
		All(ctx)
}

func (s *AssetTypeService) GetByID(ctx context.Context, id int64) (*ent.AssetType, error) {
	at, err := s.client.AssetType.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get asset type %d: %w", id, err)
	}
	return at, nil
}

func (s *AssetTypeService) Create(ctx context.Context, name string, sortOrder int) (*ent.AssetType, error) {
	at, err := s.client.AssetType.Create().
		SetName(name).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create asset type: %w", err)
	}
	return at, nil
}

func (s *AssetTypeService) Update(ctx context.Context, id int64, name string, sortOrder int) (*ent.AssetType, error) {
	at, err := s.client.AssetType.UpdateOneID(id).
		SetName(name).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update asset type %d: %w", id, err)
	}
	return at, nil
}

func (s *AssetTypeService) Delete(ctx context.Context, id int64) error {
	if err := s.client.AssetType.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete asset type %d: %w", id, err)
	}
	return nil
}
