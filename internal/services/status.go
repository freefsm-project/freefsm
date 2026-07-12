package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/ent/statusworkflow"
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

func (s *StatusService) WorkflowByObjectType(ctx context.Context, objectType string) (*ent.StatusWorkflow, error) {
	wf, err := s.client.StatusWorkflow.Query().
		Where(statusworkflow.ObjectTypeEQ(objectType)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get %s status workflow: %w", objectType, err)
	}
	return wf, nil
}

func (s *StatusService) BelongsToObjectType(ctx context.Context, statusID int64, objectType string) (bool, error) {
	if statusID <= 0 {
		return true, nil
	}
	exists, err := s.client.Status.Query().
		Where(status.IDEQ(statusID), status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType))).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("validate status object type: %w", err)
	}
	return exists, nil
}
