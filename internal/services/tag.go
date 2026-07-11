package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/tag"
)

type TagService struct {
	client *ent.Client
}

func NewTagService(client *ent.Client) *TagService {
	return &TagService{client: client}
}

func (s *TagService) ListAll(ctx context.Context) ([]*ent.Tag, error) {
	return s.client.Tag.Query().Order(ent.Asc(tag.FieldName)).All(ctx)
}

func (s *TagService) GetByID(ctx context.Context, id int64) (*ent.Tag, error) {
	t, err := s.client.Tag.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get tag %d: %w", id, err)
	}
	return t, nil
}

func (s *TagService) Create(ctx context.Context, name, color string) (*ent.Tag, error) {
	if color == "" {
		color = "#3B82F6"
	}
	t, err := s.client.Tag.Create().
		SetName(name).
		SetColor(color).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

func (s *TagService) Update(ctx context.Context, id int64, name, color string) (*ent.Tag, error) {
	b := s.client.Tag.UpdateOneID(id)
	if name != "" {
		b.SetName(name)
	}
	if color != "" {
		b.SetColor(color)
	}
	t, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update tag %d: %w", id, err)
	}
	return t, nil
}

func (s *TagService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Tag.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete tag %d: %w", id, err)
	}
	return nil
}
