package services

import (
	"context"
	"fmt"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/project"
)

type ProjectService struct {
	client *ent.Client
}

func NewProjectService(client *ent.Client) *ProjectService {
	return &ProjectService{client: client}
}

func (s *ProjectService) ListAll(ctx context.Context) ([]*ent.Project, error) {
	return s.client.Project.Query().Where(project.DeletedAtIsNil()).Order(ent.Asc(project.FieldName)).All(ctx)
}

func (s *ProjectService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Project, int, error) {
	q := s.client.Project.Query().Where(project.DeletedAtIsNil())

	if search != "" {
		q = q.Where(
			project.Or(
				project.NameContainsFold(search),
				project.DescriptionContainsFold(search),
			),
		)
	}

	if statusID > 0 {
		q = q.Where(project.StatusIDEQ(statusID))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	offset := PaginationOffset(page, perPage)

	projects, err := q.
		Order(ent.Desc(project.FieldUpdatedAt)).
		Offset(offset).
		Limit(perPage).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list projects: %w", err)
	}

	return projects, total, nil
}

func (s *ProjectService) GetByID(ctx context.Context, id int64) (*ent.Project, error) {
	p, err := s.client.Project.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get project %d: %w", id, err)
	}
	return p, nil
}

type ProjectCreateParams struct {
	CustomerID           int64
	Name                 string
	Description          string
	StatusID             int64
	LocationID           int64
	CompletionPercentage float64
	StartTime            *time.Time
	EndTime              *time.Time
	Notes                string
	CustomFields         string
}

func (s *ProjectService) Create(ctx context.Context, params ProjectCreateParams) (*ent.Project, error) {
	b := s.client.Project.Create().
		SetCustomerID(params.CustomerID).
		SetName(params.Name).
		SetDescription(params.Description).
		SetCompletionPercentage(params.CompletionPercentage).
		SetNotes(params.Notes).
		SetCustomFields(params.CustomFields)

	if params.StatusID > 0 {
		b.SetStatusID(params.StatusID)
	}
	if params.LocationID > 0 {
		b.SetLocationID(params.LocationID)
	}
	if params.StartTime != nil && !params.StartTime.IsZero() {
		b.SetStartTime(*params.StartTime)
	}
	if params.EndTime != nil && !params.EndTime.IsZero() {
		b.SetEndTime(*params.EndTime)
	}

	p, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

type ProjectUpdateParams struct {
	CustomerID           *int64
	Name                 *string
	Description          *string
	StatusID             *int64
	LocationID           *int64
	CompletionPercentage *float64
	StartTime            *time.Time
	EndTime              *time.Time
	Notes                *string
	CustomFields         *string
}

func (s *ProjectService) Update(ctx context.Context, id int64, params ProjectUpdateParams) (*ent.Project, error) {
	b := s.client.Project.UpdateOneID(id)

	if params.CustomerID != nil {
		b.SetCustomerID(*params.CustomerID)
	}
	if params.Name != nil {
		b.SetName(*params.Name)
	}
	if params.Description != nil {
		b.SetDescription(*params.Description)
	}
	if params.StatusID != nil {
		if *params.StatusID > 0 {
			b.SetStatusID(*params.StatusID)
		} else {
			b.ClearStatusID()
		}
	}
	if params.LocationID != nil {
		if *params.LocationID > 0 {
			b.SetLocationID(*params.LocationID)
		} else {
			b.ClearLocationID()
		}
	}
	if params.CompletionPercentage != nil {
		b.SetCompletionPercentage(*params.CompletionPercentage)
	}
	if params.StartTime != nil {
		if !params.StartTime.IsZero() {
			b.SetStartTime(*params.StartTime)
		} else {
			b.ClearStartTime()
		}
	}
	if params.EndTime != nil {
		if !params.EndTime.IsZero() {
			b.SetEndTime(*params.EndTime)
		} else {
			b.ClearEndTime()
		}
	}
	if params.Notes != nil {
		b.SetNotes(*params.Notes)
	}

	p, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update project %d: %w", id, err)
	}
	return p, nil
}

func (s *ProjectService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Project.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	return nil
}

func (s *ProjectService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Project.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive project %d: %w", id, err)
	}
	return nil
}

func (s *ProjectService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Project.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore project %d: %w", id, err)
	}
	return nil
}

func ProjectPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
