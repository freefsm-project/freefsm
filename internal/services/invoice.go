package services

import (
	"context"
	"fmt"
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
	CustomerID   int64
	JobID        int64
	EstimateID   int64
	StatusID     int64
	Title        string
	Notes        string
	InvoiceDate  time.Time
	DueDate      time.Time
	TaxRate      string
	LineItems    []LineItem
	CustomFields string
}

type InvoiceUpdateParams struct {
	CustomerID   *int64
	JobID        *int64
	EstimateID   *int64
	StatusID     *int64
	Title        *string
	Notes        *string
	InvoiceDate  *time.Time
	DueDate      *time.Time
	TaxRate      *string
	LineItems    *[]LineItem
	CustomFields *string
}

func (s *InvoiceService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil())

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

	offset := PaginationOffset(page, perPage)
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

func (s *InvoiceService) ListByCustomer(ctx context.Context, customerID int64, limit int) ([]*ent.Invoice, error) {
	q := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.CustomerIDEQ(customerID)).
		Order(ent.Desc(invoice.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	return q.All(ctx)
}

func (s *InvoiceService) ListForCustomer(ctx context.Context, customerID int64, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil(), invoice.CustomerIDEQ(customerID))

	if search != "" {
		q = q.Where(invoice.TitleContainsFold(search))
	}
	if statusID > 0 {
		q = q.Where(invoice.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count customer invoices: %w", err)
	}
	invoices, err := q.Order(ent.Desc(invoice.FieldCreatedAt)).Limit(perPage).Offset(PaginationOffset(page, perPage)).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list customer invoices: %w", err)
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
	if err := validateJobCustomer(ctx, s.client, params.CustomerID, params.JobID, true); err != nil {
		return nil, err
	}
	if err := validateEstimateCustomer(ctx, s.client, params.CustomerID, params.EstimateID, true); err != nil {
		return nil, err
	}

	taxRate := params.TaxRate
	if taxRate == "" {
		taxRate = "0"
	}
	customFields := params.CustomFields
	if customFields == "" {
		customFields = "[]"
	}
	b := s.client.Invoice.Create().
		SetCustomerID(params.CustomerID).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetInvoiceDate(params.InvoiceDate).
		SetDueDate(params.DueDate).
		SetTaxRate(taxRate).
		SetLineItems(SerializeLineItems(params.LineItems)).
		SetCustomFields(customFields)

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
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	customerID := int64Value(current.CustomerID)
	jobID := int64Value(current.JobID)
	estimateID := int64Value(current.EstimateID)
	if params.CustomerID != nil {
		customerID = *params.CustomerID
	}
	if params.JobID != nil {
		jobID = *params.JobID
	}
	if params.EstimateID != nil {
		estimateID = *params.EstimateID
	}
	if err := validateJobCustomer(ctx, s.client, customerID, jobID, params.JobID != nil && jobID != int64Value(current.JobID)); err != nil {
		return nil, err
	}
	if err := validateEstimateCustomer(ctx, s.client, customerID, estimateID, params.EstimateID != nil && estimateID != int64Value(current.EstimateID)); err != nil {
		return nil, err
	}

	u := s.client.Invoice.UpdateOneID(id)

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
	if params.EstimateID != nil {
		if *params.EstimateID > 0 {
			u.SetEstimateID(*params.EstimateID)
		} else {
			u.ClearEstimateID()
		}
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
	if params.LineItems != nil {
		u.SetLineItems(SerializeLineItems(*params.LineItems))
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
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

func (s *InvoiceService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Invoice.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive invoice %d: %w", id, err)
	}
	return nil
}

func (s *InvoiceService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Invoice.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore invoice %d: %w", id, err)
	}
	return nil
}

func (s *InvoiceService) Finalize(ctx context.Context, id int64, statusID int64, invoiceDate time.Time, defaultDueDays int) (*ent.Invoice, error) {
	dueDate := invoiceDate.AddDate(0, 0, defaultDueDays)
	i, err := s.client.Invoice.UpdateOneID(id).
		SetStatusID(statusID).
		SetInvoiceDate(invoiceDate).
		SetDueDate(dueDate).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("finalize invoice %d: %w", id, err)
	}
	return i, nil
}

func (s *InvoiceService) SetStatus(ctx context.Context, id int64, statusID int64) error {
	if _, err := s.client.Invoice.UpdateOneID(id).SetStatusID(statusID).Save(ctx); err != nil {
		return fmt.Errorf("set invoice status %d: %w", id, err)
	}
	return nil
}

func InvoicePaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

func (s *InvoiceService) LineItems(i *ent.Invoice) []LineItem {
	items, _ := ParseLineItems(i.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

func (s *InvoiceService) Payments(i *ent.Invoice) []Payment {
	p, _ := ParsePayments(i.Payments)
	if p == nil {
		return []Payment{}
	}
	return p
}

func (s *InvoiceService) CreateFromEstimate(ctx context.Context, estimateID int64, statusSvc *StatusService) (*ent.Invoice, error) {
	e, err := s.client.Estimate.Get(ctx, estimateID)
	if err != nil {
		return nil, fmt.Errorf("get estimate %d: %w", estimateID, err)
	}

	draftStatus, _ := statusSvc.FindByName(ctx, "invoice", "Draft")
	var statusID int64
	if draftStatus != nil {
		statusID = draftStatus.ID
	}

	items, _ := ParseLineItems(e.LineItems)
	now := time.Now()

	custID := e.CustomerID
	jobID := e.JobID
	if err := validateJobCustomer(ctx, s.client, int64Value(custID), int64Value(jobID), false); err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := tx.Estimate.DeleteOneID(estimateID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("delete estimate %d: %w", estimateID, err)
	}

	i, err := tx.Invoice.Create().
		SetID(estimateID).
		SetTitle(e.Title).
		SetNotes(e.Notes).
		SetTaxRate(e.TaxRate).
		SetLineItems(SerializeLineItems(items)).
		SetStatusID(statusID).
		SetInvoiceDate(now).
		SetDueDate(now.AddDate(0, 0, 30)).
		SetNillableCustomerID(custID).
		SetNillableJobID(jobID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return i, nil
}

func (s *InvoiceService) CreateFromJob(ctx context.Context, jobID int64, statusSvc *StatusService, defaultTaxRate string) (*ent.Invoice, error) {
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	newStatus, _ := statusSvc.FindByName(ctx, "invoice", "Draft")
	var statusID int64
	if newStatus != nil {
		statusID = newStatus.ID
	}

	items, _ := ParseLineItems(j.LineItems)
	now := time.Now()

	return s.Create(ctx, InvoiceCreateParams{
		CustomerID:   j.CustomerID,
		JobID:        j.ID,
		StatusID:     statusID,
		Title:        j.JobType,
		Notes:        j.Notes,
		InvoiceDate:  now,
		DueDate:      now.AddDate(0, 0, 30),
		TaxRate:      defaultTaxRate,
		CustomFields: "[]",
		LineItems:    items,
	})
}

func (s *InvoiceService) RecordPayment(ctx context.Context, invoiceID int64, payment Payment) error {
	i, err := s.client.Invoice.Get(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("get invoice %d: %w", invoiceID, err)
	}
	existing := s.Payments(i)
	existing = append(existing, payment)
	_, err = s.client.Invoice.UpdateOneID(invoiceID).SetPayments(SerializePayments(existing)).Save(ctx)
	if err != nil {
		return fmt.Errorf("record payment: %w", err)
	}
	return nil
}

func InvoiceAmountDue(i *ent.Invoice) (float64, float64, error) {
	items, err := ParseLineItems(i.LineItems)
	if err != nil {
		return 0, 0, fmt.Errorf("parse invoice line items: %w", err)
	}
	payments, err := ParsePayments(i.Payments)
	if err != nil {
		return 0, 0, fmt.Errorf("parse invoice payments: %w", err)
	}
	total := InvoiceTotal(items, i.TaxRate)
	paid := 0.0
	for _, payment := range payments {
		paid += payment.Amount
	}
	return total, paid, nil
}

func InvoiceTotal(items []LineItem, taxRate string) float64 {
	subtotal := 0.0
	taxableSubtotal := 0.0
	for _, item := range items {
		lineTotal := item.UnitPrice*float64(item.Quantity) - item.Discount + item.Surcharge
		subtotal += lineTotal
		if item.Taxable {
			taxableSubtotal += lineTotal
		}
	}
	tax := taxableSubtotal * parseTaxRate(taxRate) / 100
	return subtotal + tax
}
