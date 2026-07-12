package services

import (
	"context"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
)

type EstimateService struct {
	client *ent.Client
}

func NewEstimateService(client *ent.Client) *EstimateService {
	return &EstimateService{client: client}
}

type EstimateCreateParams struct {
	CustomerID   int64
	JobID        int64
	StatusID     int64
	Title        string
	Notes        string
	TaxRate      string
	LineItems    []LineItem
	CustomFields string
}

type EstimateUpdateParams struct {
	CustomerID   *int64
	JobID        *int64
	StatusID     *int64
	Title        *string
	Notes        *string
	TaxRate      *string
	LineItems    *[]LineItem
	CustomFields *string
}

func (s *EstimateService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Estimate, int, error) {
	q := s.client.Estimate.Query().Where(estimate.DeletedAtIsNil(), estimate.ConversionHiddenAtIsNil())

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

	offset := PaginationOffset(page, perPage)
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

func (s *EstimateService) ListByCustomer(ctx context.Context, customerID int64, limit int) ([]*ent.Estimate, error) {
	q := s.client.Estimate.Query().
		Where(estimate.DeletedAtIsNil(), estimate.ConversionHiddenAtIsNil(), estimate.CustomerIDEQ(customerID)).
		Order(ent.Desc(estimate.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	return q.All(ctx)
}

func (s *EstimateService) ListForCustomer(ctx context.Context, customerID int64, search string, statusID int64, page, perPage int) ([]*ent.Estimate, int, error) {
	q := s.client.Estimate.Query().Where(estimate.DeletedAtIsNil(), estimate.ConversionHiddenAtIsNil(), estimate.CustomerIDEQ(customerID))

	if search != "" {
		q = q.Where(estimate.TitleContainsFold(search))
	}
	if statusID > 0 {
		q = q.Where(estimate.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count customer estimates: %w", err)
	}
	estimates, err := q.Order(ent.Desc(estimate.FieldCreatedAt)).Limit(perPage).Offset(PaginationOffset(page, perPage)).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list customer estimates: %w", err)
	}
	return estimates, total, nil
}

func (s *EstimateService) GetByID(ctx context.Context, id int64) (*ent.Estimate, error) {
	e, err := s.client.Estimate.Query().Where(estimate.IDEQ(id), estimate.ConversionHiddenAtIsNil()).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get estimate %d: %w", id, err)
	}
	return e, nil
}

func (s *EstimateService) Create(ctx context.Context, params EstimateCreateParams) (*ent.Estimate, error) {
	taxRate := params.TaxRate
	if taxRate == "" {
		taxRate = "0"
	}
	if _, err := CalculateTotals(params.LineItems, taxRate); err != nil {
		return nil, fmt.Errorf("calculate estimate totals: %w", err)
	}
	lineItems, err := EncodeLineItems(params.LineItems)
	if err != nil {
		return nil, fmt.Errorf("encode estimate line items: %w", err)
	}
	if err := validateJobCustomer(ctx, s.client, params.CustomerID, params.JobID, true); err != nil {
		return nil, err
	}
	customer, err := s.client.Customer.Get(ctx, params.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("get estimate customer: %w", err)
	}
	if err := validateDocumentStatus(ctx, s.client, params.StatusID, customer.CompanyID, "estimate"); err != nil {
		return nil, err
	}

	customFields := params.CustomFields
	if customFields == "" {
		customFields = "[]"
	}
	b := s.client.Estimate.Create().
		SetCustomerID(params.CustomerID).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetTaxRate(taxRate).
		SetLineItems(lineItems).
		SetCustomFields(customFields)

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
	var encodedLineItems string
	if params.LineItems != nil {
		var err error
		encodedLineItems, err = EncodeLineItems(*params.LineItems)
		if err != nil {
			return nil, fmt.Errorf("encode estimate line items: %w", err)
		}
	}
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if params.LineItems != nil || params.TaxRate != nil {
		items := []LineItem(nil)
		if params.LineItems != nil {
			items = *params.LineItems
		} else {
			items, err = DecodeLineItems(current.LineItems)
			if err != nil {
				return nil, fmt.Errorf("decode estimate line items: %w", err)
			}
		}
		taxRate := current.TaxRate
		if params.TaxRate != nil {
			taxRate = *params.TaxRate
		}
		if _, err := CalculateTotals(items, taxRate); err != nil {
			return nil, fmt.Errorf("calculate estimate totals: %w", err)
		}
	}
	customerID := int64Value(current.CustomerID)
	jobID := int64Value(current.JobID)
	if params.CustomerID != nil {
		customerID = *params.CustomerID
	}
	if params.JobID != nil {
		jobID = *params.JobID
	}
	if err := validateJobCustomer(ctx, s.client, customerID, jobID, params.JobID != nil && jobID != int64Value(current.JobID)); err != nil {
		return nil, err
	}

	u := s.client.Estimate.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
	if params.JobID != nil {
		if *params.JobID > 0 {
			u.SetJobID(*params.JobID)
		} else {
			u.ClearJobID()
		}
	}
	if params.StatusID != nil {
		if err := validateDocumentStatus(ctx, s.client, *params.StatusID, current.CompanyID, "estimate"); err != nil {
			return nil, err
		}
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
		u.SetLineItems(encodedLineItems)
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

func (s *EstimateService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Estimate.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive estimate %d: %w", id, err)
	}
	return nil
}

func (s *EstimateService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Estimate.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore estimate %d: %w", id, err)
	}
	return nil
}

func EstimatePaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

func (s *EstimateService) CreateFromJob(ctx context.Context, jobID int64, statusSvc *StatusService, defaultTaxRate string) (*ent.Estimate, error) {
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	draftStatus, _ := statusSvc.DraftForObjectType(ctx, "estimate")
	var statusID int64
	if draftStatus != nil {
		statusID = draftStatus.ID
	}

	items, err := DecodeLineItems(j.LineItems)
	if err != nil {
		return nil, fmt.Errorf("parse job %d line items: %w", jobID, err)
	}

	return s.Create(ctx, EstimateCreateParams{
		CustomerID:   j.CustomerID,
		JobID:        j.ID,
		StatusID:     statusID,
		Title:        j.JobType,
		Notes:        j.Notes,
		TaxRate:      defaultTaxRate,
		CustomFields: "[]",
		LineItems:    items,
	})
}

func (s *EstimateService) LineItems(e *ent.Estimate) []LineItem {
	items, _ := DecodeLineItems(e.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

func EstimateTotal(items []LineItem, taxRate string) (float64, error) {
	totals, err := CalculateTotals(items, taxRate)
	if err != nil {
		return 0, err
	}
	return totals.Total.MajorUnits(), nil
}
