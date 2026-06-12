package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/status"
)

type StatusService struct {
	client *ent.Client
}

func NewStatusService(client *ent.Client) *StatusService {
	return &StatusService{client: client}
}

func (s *StatusService) All(ctx context.Context) ([]*ent.Status, error) {
	statuses, err := s.client.Status.Query().
		Order(ent.Asc(status.FieldSortOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list statuses: %w", err)
	}
	return statuses, nil
}
