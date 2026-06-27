package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/status"
	"github.com/MartialM1nd/freefsm/internal/ent/statusworkflow"
)

var (
	ErrReplacementStatusNeeded  = errors.New("replacement status is required")
	ErrInvalidReplacementStatus = errors.New("replacement must be a different status for the same object type")
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

func (s *StatusService) CreateForObjectType(ctx context.Context, objectType, name, color string, sortOrder int) (*ent.Status, error) {
	wf, err := s.WorkflowByObjectType(ctx, objectType)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if color == "" {
		color = "#6B7280"
	}
	st, err := s.client.Status.Create().
		SetWorkflowID(wf.ID).
		SetName(name).
		SetColor(color).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create %s status: %w", objectType, err)
	}
	return st, nil
}

func (s *StatusService) Update(ctx context.Context, id int64, name, color string, sortOrder int) (*ent.Status, error) {
	name = strings.TrimSpace(name)
	if color == "" {
		color = "#6B7280"
	}
	st, err := s.client.Status.UpdateOneID(id).
		SetName(name).
		SetColor(color).
		SetSortOrder(sortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update status %d: %w", id, err)
	}
	return st, nil
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

func (s *StatusService) CountObjectUsage(ctx context.Context, objectType string, statusID int64) (int, error) {
	switch objectType {
	case "job":
		count, err := s.client.Job.Query().Where(job.StatusIDEQ(statusID)).Count(ctx)
		if err != nil {
			return 0, fmt.Errorf("count jobs using status: %w", err)
		}
		return count, nil
	default:
		return 0, fmt.Errorf("unsupported status object type %q", objectType)
	}
}

func (s *StatusService) Delete(ctx context.Context, objectType string, statusID int64, replacementStatusID *int64) error {
	ok, err := s.BelongsToObjectType(ctx, statusID, objectType)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("status %d does not belong to %s", statusID, objectType)
	}
	usage, err := s.CountObjectUsage(ctx, objectType, statusID)
	if err != nil {
		return err
	}
	if usage > 0 {
		if replacementStatusID == nil || *replacementStatusID <= 0 {
			return ErrReplacementStatusNeeded
		}
		if *replacementStatusID == statusID {
			return ErrInvalidReplacementStatus
		}
		ok, err := s.BelongsToObjectType(ctx, *replacementStatusID, objectType)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalidReplacementStatus
		}
		if err := s.reassignStatus(ctx, objectType, statusID, *replacementStatusID); err != nil {
			return err
		}
	}
	if err := s.client.Status.DeleteOneID(statusID).Exec(ctx); err != nil {
		return fmt.Errorf("delete status %d: %w", statusID, err)
	}
	return nil
}

func (s *StatusService) reassignStatus(ctx context.Context, objectType string, statusID, replacementStatusID int64) error {
	switch objectType {
	case "job":
		if _, err := s.client.Job.Update().Where(job.StatusIDEQ(statusID)).SetStatusID(replacementStatusID).Save(ctx); err != nil {
			return fmt.Errorf("reassign jobs to replacement status: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported status object type %q", objectType)
	}
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
