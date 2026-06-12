package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/status"
	"github.com/MartialM1nd/freefsm/internal/ent/statusworkflow"
)

type StatusService struct {
	client *ent.Client
}

func NewStatusService(client *ent.Client) *StatusService {
	return &StatusService{client: client}
}

func (s *StatusService) ByObjectType(ctx context.Context, objectType string) ([]*ent.Status, error) {
	statuses, err := s.client.Status.Query().
		Where(status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType))).
		Order(ent.Asc(status.FieldSortOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list statuses: %w", err)
	}
	return statuses, nil
}

func (s *StatusService) FindByName(ctx context.Context, objectType, name string) (*ent.Status, error) {
	st, err := s.client.Status.Query().
		Where(
			status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType)),
			status.NameEQ(name),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("find status %s in %s: %w", name, objectType, err)
	}
	return st, nil
}
