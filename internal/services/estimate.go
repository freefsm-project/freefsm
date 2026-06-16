package services

import (
	"context"
	"fmt"
	"math"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/estimate"
)

type EstimateService struct {
	client *ent.Client
}

func NewEstimateService(client *ent.Client) *EstimateService {
	return &EstimateService{client: client}
}

type EstimateCreateParams struct {
	CustomerID int64
	JobID      int64
	StatusID   int64
	Title      string
	Notes      string
	TaxRate    string
	LineItems  []LineItem
	CustomFields string
}

type EstimateUpdateParams struct {
	CustomerID *int64
	JobID      *int64
	StatusID   *int64
	Title      *string
	Notes      *string
	TaxRate    *string
	LineItems  *[]LineItem
	CustomFields *string
}

func (s *EstimateService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Estimate, int, error) {
	q := s.client.Estimate.Query()

	if search != "" {
		q = q.Where(estimate.TitleContainsFold(search))
	}

	if statusID > 0 {
		q = q.Where(estimate.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count estimates: %w", err)
	}

	offset := (page - 1) * perPage
	estimates, err := q.
		Order(ent.Desc(estimate.FieldCreatedAt)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list estimates: %w", err)
	}

	return estimates, total, nil
}

func (s *EstimateService) GetByID(ctx context.Context, id int64) (*ent.Estimate, error) {
	e, err := s.client.Estimate.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get estimate %d: %w", id, err)
	}
	return e, nil
}

func (s *EstimateService) Create(ctx context.Context, params EstimateCreateParams) (*ent.Estimate, error) {
	b := s.client.Estimate.Create().
		SetCustomerID(params.CustomerID).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetTaxRate(params.TaxRate).
		SetLineItems(SerializeLineItems(params.LineItems)).
		SetCustomFields(params.CustomFields)

	if params.JobID > 0 {
		b.SetJobID(params.JobID)
	}
	if params.StatusID > 0 {
		b.SetStatusID(params.StatusID)
	}

	e, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create estimate: %w", err)
	}
	return e, nil
}

func (s *EstimateService) Update(ctx context.Context, id int64, params EstimateUpdateParams) (*ent.Estimate, error) {
	u := s.client.Estimate.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
	if params.JobID != nil {
		u.SetJobID(*params.JobID)
	}
	if params.StatusID != nil {
		u.SetStatusID(*params.StatusID)
	}
	if params.Title != nil {
		u.SetTitle(*params.Title)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.TaxRate != nil {
		u.SetTaxRate(*params.TaxRate)
	}
	if params.LineItems != nil {
		u.SetLineItems(SerializeLineItems(*params.LineItems))
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
	}

	e, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update estimate %d: %w", id, err)
	}
	return e, nil
}

func (s *EstimateService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Estimate.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete estimate %d: %w", id, err)
	}
	return nil
}

func EstimatePaginationTotalPages(total, perPage int) int {
	return int(math.Ceil(float64(total) / float64(perPage)))
}

func (s *EstimateService) CreateFromJob(ctx context.Context, jobID int64, statusSvc *StatusService) (*ent.Estimate, error) {
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	draftStatus, err := statusSvc.FindByName(ctx, "estimate", "Draft")
	if err != nil {
		return nil, fmt.Errorf("find status: %w", err)
	}

	items, _ := ParseLineItems(j.LineItems)

	return s.Create(ctx, EstimateCreateParams{
		CustomerID: j.CustomerID,
		JobID:      j.ID,
		StatusID:   draftStatus.ID,
		Title:      j.JobType,
		Notes:      j.Notes,
		TaxRate:    "0",
		LineItems:  items,
	})
}

func (s *EstimateService) LineItems(e *ent.Estimate) []LineItem {
	items, _ := ParseLineItems(e.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}
