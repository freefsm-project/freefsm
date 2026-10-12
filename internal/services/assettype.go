package services

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"

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
	if req, ok := authorization.FromContext(ctx); ok {
		id, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_type", 0, "save", authorization.VocabularyInput{Name: name, SortOrder: sortOrder})
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
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
	if req, ok := authorization.FromContext(ctx); ok {
		_, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_type", id, "save", authorization.VocabularyInput{Name: name, SortOrder: sortOrder})
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
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
	if req, ok := authorization.FromContext(ctx); ok {
		_, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_type", id, "delete", authorization.VocabularyInput{})
		return err
	}
	if err := s.client.AssetType.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete asset type %d: %w", id, err)
	}
	return nil
}
