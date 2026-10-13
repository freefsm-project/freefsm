package services

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/jackc/pgx/v5"
)

type ProjectService struct {
	client *ent.Client
}

type ProjectValidationError struct{ Field, Message string }

func (e *ProjectValidationError) Error() string { return e.Message }

func NewProjectService(client *ent.Client) *ProjectService {
	return &ProjectService{client: client}
}

func (s *ProjectService) ListAll(ctx context.Context) ([]*ent.Project, error) {
	return s.client.Project.Query().Where(project.DeletedAtIsNil()).Order(ent.Asc(project.FieldName)).All(ctx)
}

func (s *ProjectService) ListByCustomer(ctx context.Context, customerID int64) ([]*ent.Project, error) {
	return s.client.Project.Query().
		Where(project.DeletedAtIsNil(), project.CustomerIDEQ(customerID)).
		Order(ent.Asc(project.FieldName)).
		All(ctx)
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
	ServiceNotes         string
	CustomFields         string
}

func (s *ProjectService) Create(ctx context.Context, companyID int64, params ProjectCreateParams) (*ent.Project, error) {
	if strings.TrimSpace(params.Name) == "" {
		return nil, &ProjectValidationError{Field: "name", Message: "Project name is required"}
	}
	if err := validateProjectDates(params.StartTime, params.EndTime); err != nil {
		return nil, err
	}
	if needsDetailsTransaction(ctx) {
		return saveDetails(ctx, "project", 0, 0, &params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Project, int64, error) {
			params.CustomFields = fields
			v, err := NewProjectService(c).Create(ctx, companyID, params)
			if err != nil {
				return nil, 0, err
			}
			return v, v.ID, nil
		})
	}
	if tx, ok := ctx.Value(detailsTxKey{}).(pgx.Tx); ok {
		r, _ := authorization.FromContext(ctx)
		if err := r.Service.ProjectInputs(ctx, tx, r.Actor, 0, params.CustomerID, params.LocationID, true); err != nil {
			return nil, err
		}
	}
	projectCustomer, err := activeCustomerOwnedByCompany(ctx, s.client, companyID, params.CustomerID)
	if err != nil {
		return nil, err
	}
	if err := validateCustomerLocationForCompany(ctx, s.client, companyID, params.CustomerID, params.LocationID); err != nil {
		return nil, err
	}
	statusID, err := creationStatus(ctx, s.client, 0, projectCustomer.CompanyID, "project", "project:new")
	if err != nil {
		return nil, err
	}

	b := s.client.Project.Create().
		SetCompanyID(companyID).
		SetCustomerID(params.CustomerID).
		SetName(params.Name).
		SetDescription(params.Description).
		SetCompletionPercentage(0).
		SetNotes(params.Notes).
		SetServiceNotes(params.ServiceNotes).
		SetCustomFields(params.CustomFields)

	b.SetStatusID(statusID)
	if params.LocationID > 0 {
		b.SetLocationID(params.LocationID)
	}
	if params.StartTime != nil && !params.StartTime.IsZero() {
		b.SetPlannedStartDate(*params.StartTime)
	}
	if params.EndTime != nil && !params.EndTime.IsZero() {
		b.SetPlannedEndDate(*params.EndTime)
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
	LocationID           *int64
	CompletionPercentage *float64
	StartTime            *time.Time
	EndTime              *time.Time
	Notes                *string
	ServiceNotes         *string
	CustomFields         *string
}

func (s *ProjectService) Update(ctx context.Context, companyID, id int64, params ProjectUpdateParams) (*ent.Project, error) {
	if _, ok := authorization.FromContext(ctx); ok {
		return s.SaveDetails(ctx, companyID, id, params)
	}
	return s.update(ctx, companyID, id, params)
}

func (s *ProjectService) SaveDetails(ctx context.Context, companyID, id int64, params ProjectUpdateParams) (*ent.Project, error) {
	return saveDetails(ctx, "project", id, 0, params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Project, int64, error) {
		params.CustomFields = &fields
		v, err := NewProjectService(c).update(ctx, companyID, id, params)
		return v, id, err
	})
}

func (s *ProjectService) update(ctx context.Context, companyID, id int64, params ProjectUpdateParams) (*ent.Project, error) {
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.CompanyID != nil && *current.CompanyID != companyID {
		return nil, fmt.Errorf("project does not belong to company")
	}
	if params.Name != nil && strings.TrimSpace(*params.Name) == "" {
		return nil, &ProjectValidationError{Field: "name", Message: "Project name is required"}
	}
	if params.CompletionPercentage != nil {
		v := *params.CompletionPercentage
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 || math.Trunc(v) != v {
			return nil, &ProjectValidationError{Field: "completion_percentage", Message: "Progress must be an integer from 0 through 100"}
		}
		if current.StatusID != nil {
			status, err := s.client.Status.Get(ctx, *current.StatusID)
			if err != nil {
				return nil, err
			}
			if status.CategoryKey == "project:completed" && v != current.CompletionPercentage {
				return nil, authorization.ErrConflict
			}
		}
	}
	start, end := current.PlannedStartDate, current.PlannedEndDate
	if params.StartTime != nil {
		start = params.StartTime
	}
	if params.EndTime != nil {
		end = params.EndTime
	}
	if err := validateProjectDates(start, end); err != nil {
		return nil, err
	}
	customerID := current.CustomerID
	locationID := int64Value(current.LocationID)
	if params.CustomerID != nil {
		if *params.CustomerID != customerID {
			return nil, &ProjectValidationError{Field: "customer_id", Message: "Project Customer cannot be changed"}
		}
	}
	if params.LocationID != nil {
		locationID = *params.LocationID
	}
	if tx, ok := ctx.Value(detailsTxKey{}).(pgx.Tx); ok {
		r, _ := authorization.FromContext(ctx)
		if err := r.Service.ProjectInputs(ctx, tx, r.Actor, id, customerID, locationID, locationID != int64Value(current.LocationID)); err != nil {
			return nil, err
		}
	} else if err := validateCustomerLocationForCompany(ctx, s.client, companyID, customerID, locationID); err != nil {
		return nil, err
	}
	if _, err := activeCustomerOwnedByCompany(ctx, s.client, companyID, customerID); err != nil {
		return nil, err
	}

	b := s.client.Project.UpdateOneID(id).SetCompanyID(companyID)

	if params.CustomerID != nil {
		b.SetCustomerID(*params.CustomerID)
	}
	if params.Name != nil {
		b.SetName(*params.Name)
	}
	if params.Description != nil {
		b.SetDescription(*params.Description)
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
			b.SetPlannedStartDate(*params.StartTime)
		} else {
			b.ClearPlannedStartDate()
		}
	}
	if params.EndTime != nil {
		if !params.EndTime.IsZero() {
			b.SetPlannedEndDate(*params.EndTime)
		} else {
			b.ClearPlannedEndDate()
		}
	}
	if params.Notes != nil {
		b.SetNotes(*params.Notes)
	}
	if params.ServiceNotes != nil {
		b.SetServiceNotes(*params.ServiceNotes)
	}
	if params.CustomFields != nil {
		b.SetCustomFields(*params.CustomFields)
	}

	p, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update project %d: %w", id, err)
	}
	return p, nil
}

func validateProjectDates(start, end *time.Time) error {
	if start != nil && end != nil && !start.IsZero() && !end.IsZero() && start.UTC().Format("2006-01-02") > end.UTC().Format("2006-01-02") {
		return &ProjectValidationError{Field: "end_time", Message: "Planned end date must not precede planned start date"}
	}
	return nil
}

func (s *ProjectService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Project.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	return nil
}

func (s *ProjectService) Archive(ctx context.Context, id int64) error {
	if r, ok := authorization.FromContext(ctx); ok {
		return r.Service.ProjectLifecycle(ctx, r.Actor, id, false)
	}
	now := time.Now()
	_, err := s.client.Project.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive project %d: %w", id, err)
	}
	return nil
}

func (s *ProjectService) Restore(ctx context.Context, id int64) error {
	if r, ok := authorization.FromContext(ctx); ok {
		return r.Service.ProjectLifecycle(ctx, r.Actor, id, true)
	}
	_, err := s.client.Project.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore project %d: %w", id, err)
	}
	return nil
}

func ProjectPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
