package services

import (
	"context"

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
