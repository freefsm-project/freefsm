package services

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/assetstatus"
)

type AssetStatusService struct {
	client *ent.Client
}

func NewAssetStatusService(client *ent.Client) *AssetStatusService {
	return &AssetStatusService{client: client}
}

func (s *AssetStatusService) List(ctx context.Context) ([]*ent.AssetStatus, error) {
	q := s.client.AssetStatus.Query()
	if req, ok := authorization.FromContext(ctx); ok {
		p, err := req.Service.Principal(ctx, req.Actor)
		if err != nil {
			return nil, err
		}
		q.Where(assetstatus.Or(assetstatus.CompanyIDEQ(p.CompanyID), assetstatus.CompanyIDIsNil()))
	}
	return q.
		Order(ent.Asc(assetstatus.FieldSortOrder)).
		All(ctx)
}

func (s *AssetStatusService) GetByID(ctx context.Context, id int64) (*ent.AssetStatus, error) {
	as, err := s.client.AssetStatus.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get asset status %d: %w", id, err)
	}
	return as, nil
}

func (s *AssetStatusService) Create(ctx context.Context, name, color string, sortOrder int) (*ent.AssetStatus, error) {
	if req, ok := authorization.FromContext(ctx); ok {
		id, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_status", 0, "save", authorization.VocabularyInput{Name: name, Color: color, SortOrder: sortOrder})
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
	as, err := s.client.AssetStatus.Create().
		SetName(name).
		SetColor(color).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create asset status: %w", err)
	}
	return as, nil
}

func (s *AssetStatusService) Update(ctx context.Context, id int64, name, color string, sortOrder int) (*ent.AssetStatus, error) {
	if req, ok := authorization.FromContext(ctx); ok {
		_, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_status", id, "save", authorization.VocabularyInput{Name: name, Color: color, SortOrder: sortOrder})
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
	as, err := s.client.AssetStatus.UpdateOneID(id).
		SetName(name).
		SetColor(color).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update asset status %d: %w", id, err)
	}
	return as, nil
}

func (s *AssetStatusService) Delete(ctx context.Context, id int64) error {
	if req, ok := authorization.FromContext(ctx); ok {
		_, err := req.Service.ChangeVocabulary(ctx, req.Actor, "asset_status", id, "delete", authorization.VocabularyInput{})
		return err
	}
	if err := s.client.AssetStatus.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete asset status %d: %w", id, err)
	}
	return nil
}
