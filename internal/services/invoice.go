package services

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/invoice"
)

type InvoiceService struct {
	client *ent.Client
}

func NewInvoiceService(client *ent.Client) *InvoiceService {
	return &InvoiceService{client: client}
}

type InvoiceCreateParams struct {
	CustomerID  int64
	JobID       int64
	EstimateID  int64
	StatusID    int64
	Title       string
	Notes       string
	InvoiceDate time.Time
	DueDate     time.Time
	TaxRate     string
}

type InvoiceUpdateParams struct {
	CustomerID  *int64
	JobID       *int64
	EstimateID  *int64
	StatusID    *int64
	Title       *string
	Notes       *string
	InvoiceDate *time.Time
	DueDate     *time.Time
	TaxRate     *string
}

func (s *InvoiceService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query()

	if search != "" {
		q = q.Where(invoice.TitleContainsFold(search))
	}

	if statusID > 0 {
		q = q.Where(invoice.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count invoices: %w", err)
	}

	offset := (page - 1) * perPage
	invoices, err := q.
		Order(ent.Desc(invoice.FieldCreatedAt)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list invoices: %w", err)
	}

	return invoices, total, nil
}

func (s *InvoiceService) GetByID(ctx context.Context, id int64) (*ent.Invoice, error) {
	i, err := s.client.Invoice.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get invoice %d: %w", id, err)
	}
	return i, nil
}

func (s *InvoiceService) Create(ctx context.Context, params InvoiceCreateParams) (*ent.Invoice, error) {
	b := s.client.Invoice.Create().
		SetCustomerID(params.CustomerID).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetInvoiceDate(params.InvoiceDate).
		SetDueDate(params.DueDate).
		SetTaxRate(params.TaxRate)

	if params.JobID > 0 {
		b.SetJobID(params.JobID)
	}
	if params.EstimateID > 0 {
		b.SetEstimateID(params.EstimateID)
	}
	if params.StatusID > 0 {
		b.SetStatusID(params.StatusID)
	}

	i, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	return i, nil
}

func (s *InvoiceService) Update(ctx context.Context, id int64, params InvoiceUpdateParams) (*ent.Invoice, error) {
	u := s.client.Invoice.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
	if params.JobID != nil {
		u.SetJobID(*params.JobID)
	}
	if params.EstimateID != nil {
		u.SetEstimateID(*params.EstimateID)
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
	if params.InvoiceDate != nil {
		u.SetInvoiceDate(*params.InvoiceDate)
	}
	if params.DueDate != nil {
		u.SetDueDate(*params.DueDate)
	}
	if params.TaxRate != nil {
		u.SetTaxRate(*params.TaxRate)
	}

	i, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update invoice %d: %w", id, err)
	}
	return i, nil
}

func (s *InvoiceService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Invoice.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete invoice %d: %w", id, err)
	}
	return nil
}

func InvoicePaginationTotalPages(total, perPage int) int {
	return int(math.Ceil(float64(total) / float64(perPage)))
}
