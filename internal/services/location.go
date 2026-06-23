package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/location"
)

type LocationService struct {
	client *ent.Client
}

func NewLocationService(client *ent.Client) *LocationService {
	return &LocationService{client: client}
}

func (s *LocationService) ListAll(ctx context.Context) ([]*ent.Location, error) {
	return s.client.Location.Query().Order(ent.Asc(location.FieldTitle)).All(ctx)
}

func (s *LocationService) GetByID(ctx context.Context, id int64) (*ent.Location, error) {
	l, err := s.client.Location.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get location %d: %w", id, err)
	}
	return l, nil
}
