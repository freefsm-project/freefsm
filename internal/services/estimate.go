package services

import (
	"context"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
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
	ValidThrough *time.Time
	Terms        string
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
	ValidThrough      *time.Time
	ClearValidThrough bool
	Terms             *string
	CustomerID        *int64
	JobID             *int64
	Title             *string
	Notes             *string
	TaxRate           *string
	LineItems         *[]LineItem
	CustomFields      *string
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
	if needsDetailsTransaction(ctx) {
		return saveDetails(ctx, "estimate", 0, params.JobID, &params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Estimate, int64, error) {
			params.CustomFields = fields
			v, err := NewEstimateService(c).Create(ctx, params)
			if err != nil {
				return nil, 0, err
			}
			return v, v.ID, nil
		})
	}
	var customerCompanyID *int64
	if request, ok := authorization.FromContext(ctx); ok {
		input, err := request.Service.DocumentCustomer(ctx, request.Actor, params.CustomerID, params.JobID, params.TaxRate)
		if err != nil {
			return nil, err
		}
		customerCompanyID = &input.CompanyID
		params.TaxRate = input.TaxRate
	}
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
	if customerCompanyID == nil {
		customer, err := s.client.Customer.Get(ctx, params.CustomerID)
		if err != nil {
			return nil, fmt.Errorf("get estimate customer: %w", err)
		}
		customerCompanyID = customer.CompanyID
	}
	statusID, err := creationStatus(ctx, s.client, 0, customerCompanyID, "estimate", "estimate:draft")
	if err != nil {
		return nil, err
	}

	customFields := params.CustomFields
	if customFields == "" {
		customFields = "[]"
	}
	if request, ok := authorization.FromContext(ctx); ok {
		id, err := createDocument(ctx, request, "estimate", authorization.DocumentCreation{CustomerID: params.CustomerID, JobID: params.JobID, Title: params.Title, Notes: params.Notes, Terms: params.Terms, TaxRate: taxRate, LineItems: lineItems, CustomFields: customFields, ValidThrough: params.ValidThrough}, func(rate string) (string, error) { _, err := CalculateTotals(params.LineItems, rate); return "", err })
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
	b := s.client.Estimate.Create().
		SetNillableCompanyID(customerCompanyID).
		SetCustomerID(params.CustomerID).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetTerms(params.Terms).
		SetNillableValidThrough(params.ValidThrough).
		SetTaxRate(taxRate).
		SetLineItems(lineItems).
		SetCustomFields(customFields)

	if params.JobID > 0 {
		b.SetJobID(params.JobID)
	}
	b.SetStatusID(statusID)

	e, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create estimate: %w", err)
	}
	return e, nil
}

func (s *EstimateService) Update(ctx context.Context, id int64, params EstimateUpdateParams) (*ent.Estimate, error) {
	if _, ok := authorization.FromContext(ctx); ok {
		return s.SaveDetails(ctx, id, params)
	}
	return s.update(ctx, id, params)
}

func (s *EstimateService) SaveDetails(ctx context.Context, id int64, params EstimateUpdateParams) (*ent.Estimate, error) {
	return saveDetails(ctx, "estimate", id, 0, params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Estimate, int64, error) {
		params.CustomFields = &fields
		v, err := NewEstimateService(c).update(ctx, id, params)
		return v, id, err
	})
}

func (s *EstimateService) update(ctx context.Context, id int64, params EstimateUpdateParams) (*ent.Estimate, error) {
	if needsCatalogTransaction(ctx) && params.LineItems != nil {
		return catalogWrite(ctx, s.client, "estimate", id, *params.LineItems, func(ctx context.Context, c *ent.Client) (*ent.Estimate, error) {
			return NewEstimateService(c).update(ctx, id, params)
		})
	}
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
	if params.ValidThrough != nil {
		u.SetValidThrough(*params.ValidThrough)
	} else if params.ClearValidThrough {
		u.ClearValidThrough()
	}
	if params.Terms != nil {
		u.SetTerms(*params.Terms)
	}

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

func (s *EstimateService) CreateFromJob(ctx context.Context, jobID int64, statusSvc *StatusService, defaultTaxRate string, fields ...string) (*ent.Estimate, error) {
	ctx = authorization.WithCapturedItemSource(ctx, "job", jobID)
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	items, err := DecodeLineItems(j.LineItems)
	if err != nil {
		return nil, fmt.Errorf("parse job %d line items: %w", jobID, err)
	}

	cs, err := s.client.CompanySettings.Query().First(ctx)
	if err != nil {
		return nil, err
	}
	var customerDefaults map[string]*string
	if request, ok := authorization.FromContext(ctx); ok {
		customerDefaults, err = request.Service.DocumentDefaultInputs(ctx, request.Actor, "estimate", j.CustomerID, j.ID)
		if err != nil {
			return nil, err
		}
	}
	defaults := ResolveDocumentDefaults(cs, "estimate", customerDefaults)
	notes := j.Notes
	if notes == "" {
		notes = defaults.Notes
	}
	var validThrough *time.Time
	if defaults.Days != nil {
		day := time.Now().In(companySettingsLocation(cs)).AddDate(0, 0, *defaults.Days)
		date, _ := time.Parse("2006-01-02", day.Format("2006-01-02"))
		validThrough = &date
	}
	values := "[]"
	if len(fields) > 0 {
		values = fields[0]
	}
	return s.Create(ctx, EstimateCreateParams{
		CustomerID:   j.CustomerID,
		JobID:        j.ID,
		StatusID:     0,
		Title:        j.JobType,
		Notes:        notes,
		Terms:        defaults.Terms,
		ValidThrough: validThrough,
		TaxRate:      defaultTaxRate,
		CustomFields: values,
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
