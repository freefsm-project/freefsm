package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNegativeInvoiceTotal = errors.New("invoice total must not be negative")

func invoiceSettlementState(totalCents, settledCents int64) string {
	if totalCents == 0 || settledCents >= totalCents {
		return "paid"
	}
	if settledCents > 0 {
		return "partially_paid"
	}
	return "unpaid"
}

type InvoiceService struct {
	client *ent.Client
	db     *pgxpool.Pool
}

func NewInvoiceService(client *ent.Client, db ...*pgxpool.Pool) *InvoiceService {
	s := &InvoiceService{client: client}
	if len(db) > 0 {
		s.db = db[0]
	}
	return s
}

type InvoiceCreateParams struct {
	InvoiceNumber *int64
	CustomerID    int64
	JobID         int64
	EstimateID    int64
	StatusID      int64
	Title         string
	Notes         string
	InvoiceDate   time.Time
	DueDate       time.Time
	TaxRate       string
	LineItems     []LineItem
	CustomFields  string
}

type InvoiceUpdateParams struct {
	InvoiceNumber *int64
	CustomerID    *int64
	JobID         *int64
	EstimateID    *int64
	StatusID      *int64
	Title         *string
	Notes         *string
	InvoiceDate   *time.Time
	DueDate       *time.Time
	TaxRate       *string
	LineItems     *[]LineItem
	CustomFields  *string
}

func (s *InvoiceService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil(), invoice.ConversionHiddenAtIsNil())

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
		Where(invoice.DeletedAtIsNil(), invoice.ConversionHiddenAtIsNil(), invoice.CustomerIDEQ(customerID)).
		Order(ent.Desc(invoice.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	return q.All(ctx)
}

func (s *InvoiceService) LatestByJobIDs(ctx context.Context, jobIDs []int64) (map[int64]*ent.Invoice, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	invoices, err := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.ConversionHiddenAtIsNil(), invoice.JobIDIn(jobIDs...)).
		Order(ent.Desc(invoice.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list invoices by jobs: %w", err)
	}
	latest := make(map[int64]*ent.Invoice, len(jobIDs))
	for _, inv := range invoices {
		if inv.JobID == nil {
			continue
		}
		if _, ok := latest[*inv.JobID]; !ok {
			latest[*inv.JobID] = inv
		}
	}
	return latest, nil
}

func (s *InvoiceService) ListForCustomer(ctx context.Context, customerID int64, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil(), invoice.ConversionHiddenAtIsNil(), invoice.CustomerIDEQ(customerID))

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
	i, err := s.client.Invoice.Query().Where(invoice.IDEQ(id), invoice.ConversionHiddenAtIsNil()).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get invoice %d: %w", id, err)
	}
	return i, nil
}

func (s *InvoiceService) Create(ctx context.Context, params InvoiceCreateParams) (*ent.Invoice, error) {
	taxRate := params.TaxRate
	if taxRate == "" {
		taxRate = "0"
	}
	totals, err := CalculateTotals(params.LineItems, taxRate)
	if err != nil {
		return nil, fmt.Errorf("calculate invoice totals: %w", err)
	}
	if totals.Total.MinorUnits() < 0 {
		return nil, ErrNegativeInvoiceTotal
	}
	lineItems, err := EncodeLineItems(params.LineItems)
	if err != nil {
		return nil, fmt.Errorf("encode invoice line items: %w", err)
	}
	if err := validateJobCustomer(ctx, s.client, params.CustomerID, params.JobID, true); err != nil {
		return nil, err
	}
	if err := validateEstimateCustomer(ctx, s.client, params.CustomerID, params.EstimateID, true); err != nil {
		return nil, err
	}
	customer, err := s.client.Customer.Get(ctx, params.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("get invoice customer: %w", err)
	}
	if err := validateDocumentStatus(ctx, s.client, params.StatusID, customer.CompanyID, "invoice"); err != nil {
		return nil, err
	}

	customFields := params.CustomFields
	if customFields == "" {
		customFields = "[]"
	}
	if s.db != nil {
		return s.createTransactionally(ctx, params, taxRate, lineItems, customFields, totals.Total.MinorUnits())
	}
	b := s.client.Invoice.Create().
		SetCustomerID(params.CustomerID).
		SetSettlementState(invoiceSettlementState(totals.Total.MinorUnits(), 0)).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetInvoiceDate(params.InvoiceDate).
		SetDueDate(params.DueDate).
		SetTaxRate(taxRate).
		SetLineItems(lineItems).
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
	if err := s.assignInvoiceNumber(ctx, b, params.InvoiceNumber); err != nil {
		return nil, err
	}

	i, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	return i, nil
}

func (s *InvoiceService) createTransactionally(ctx context.Context, params InvoiceCreateParams, taxRate, lineItems, customFields string, totalCents int64) (*ent.Invoice, error) {
	cs, err := s.client.CompanySettings.Query().First(ctx)
	if err != nil || cs.CompanyID == nil {
		return nil, fmt.Errorf("load company settings: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var next int64
	if err = tx.QueryRow(ctx, `SELECT next_invoice_number FROM company_settings WHERE company_id=$1 FOR UPDATE`, *cs.CompanyID).Scan(&next); err != nil {
		return nil, err
	}
	number := next
	if params.InvoiceNumber != nil {
		number = *params.InvoiceNumber
	}
	if number <= 0 {
		return nil, errors.New("invoice number must be greater than zero")
	}
	var used bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices WHERE company_id=$1 AND invoice_number=$2)`, *cs.CompanyID, number).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, fmt.Errorf("invoice number %d is already in use", number)
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,estimate_id,status_id,invoice_number,title,notes,invoice_date,due_date,tax_rate,line_items,custom_fields,settlement_state)
		VALUES($1,$2,NULLIF($3,0),NULLIF($4,0),NULLIF($5,0),$6,$7,$8,$9,$10,$11,$12::jsonb,$13::jsonb,$14) RETURNING id`,
		*cs.CompanyID, params.CustomerID, params.JobID, params.EstimateID, params.StatusID, number, params.Title, params.Notes, params.InvoiceDate, params.DueDate, taxRate, lineItems, customFields, invoiceSettlementState(totalCents, 0)).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	if number >= next {
		if _, err = tx.Exec(ctx, `UPDATE company_settings SET next_invoice_number=$1 WHERE company_id=$2`, number+1, *cs.CompanyID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func (s *InvoiceService) Update(ctx context.Context, id int64, params InvoiceUpdateParams) (*ent.Invoice, error) {
	var encodedLineItems string
	if params.LineItems != nil {
		var err error
		encodedLineItems, err = EncodeLineItems(*params.LineItems)
		if err != nil {
			return nil, fmt.Errorf("encode invoice line items: %w", err)
		}
	}
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var updatedTotalCents int64
	var currentTotalCents int64
	monetaryUpdate := params.LineItems != nil || params.TaxRate != nil
	if monetaryUpdate {
		items := []LineItem(nil)
		if params.LineItems != nil {
			items = *params.LineItems
		} else {
			items, err = DecodeLineItems(current.LineItems)
			if err != nil {
				return nil, fmt.Errorf("decode invoice line items: %w", err)
			}
		}
		taxRate := current.TaxRate
		if params.TaxRate != nil {
			taxRate = *params.TaxRate
		}
		totals, err := CalculateTotals(items, taxRate)
		if err != nil {
			return nil, fmt.Errorf("calculate invoice totals: %w", err)
		}
		if totals.Total.MinorUnits() < 0 {
			return nil, ErrNegativeInvoiceTotal
		}
		updatedTotalCents = totals.Total.MinorUnits()
		currentItems, decodeErr := DecodeLineItems(current.LineItems)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode current invoice line items: %w", decodeErr)
		}
		currentTotals, totalErr := CalculateTotals(currentItems, current.TaxRate)
		if totalErr != nil {
			return nil, fmt.Errorf("calculate current invoice totals: %w", totalErr)
		}
		currentTotalCents = currentTotals.Total.MinorUnits()
	}
	customerID := current.CustomerID
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
	if params.InvoiceNumber != nil {
		if err := s.validateInvoiceNumber(ctx, current.CompanyID, id, *params.InvoiceNumber); err != nil {
			return nil, err
		}
		u.SetInvoiceNumber(*params.InvoiceNumber)
		if err := s.bumpNextInvoiceNumber(ctx, current.CompanyID, *params.InvoiceNumber); err != nil {
			return nil, err
		}
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
	if params.EstimateID != nil {
		if *params.EstimateID > 0 {
			u.SetEstimateID(*params.EstimateID)
		} else {
			u.ClearEstimateID()
		}
	}
	if params.StatusID != nil {
		if err := validateDocumentStatus(ctx, s.client, *params.StatusID, current.CompanyID, "invoice"); err != nil {
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
		u.SetLineItems(encodedLineItems)
	}
	if monetaryUpdate && current.SettlementState != "partially_paid" && (current.SettlementState != "paid" || currentTotalCents == 0) {
		u.SetSettlementState(invoiceSettlementState(updatedTotalCents, 0))
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

func (s *InvoiceService) assignInvoiceNumber(ctx context.Context, b *ent.InvoiceCreate, requested *int64) error {
	cs, err := s.client.CompanySettings.Query().First(ctx)
	if err != nil {
		return fmt.Errorf("load company settings: %w", err)
	}
	if cs.CompanyID != nil {
		b.SetCompanyID(*cs.CompanyID)
	}
	companyID := cs.CompanyID
	number := cs.NextInvoiceNumber
	if requested != nil {
		number = *requested
	}
	if err := s.validateInvoiceNumber(ctx, companyID, 0, number); err != nil {
		return err
	}
	b.SetInvoiceNumber(number)
	return s.bumpNextInvoiceNumber(ctx, companyID, number)
}

func (s *InvoiceService) validateInvoiceNumber(ctx context.Context, companyID *int64, excludeID, number int64) error {
	if number <= 0 {
		return fmt.Errorf("invoice number must be greater than zero")
	}
	q := s.client.Invoice.Query().Where(invoice.InvoiceNumberEQ(number))
	if companyID != nil {
		q = q.Where(invoice.CompanyIDEQ(*companyID))
	} else {
		q = q.Where(invoice.CompanyIDIsNil())
	}
	if excludeID > 0 {
		q = q.Where(invoice.IDNEQ(excludeID))
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("check invoice number: %w", err)
	}
	if exists {
		return fmt.Errorf("invoice number %d is already in use", number)
	}
	return nil
}

func (s *InvoiceService) bumpNextInvoiceNumber(ctx context.Context, companyID *int64, used int64) error {
	q := s.client.CompanySettings.Query()
	if companyID != nil {
		q = q.Where(companysettings.CompanyIDEQ(*companyID))
	} else {
		q = q.Where(companysettings.CompanyIDIsNil())
	}
	cs, err := q.First(ctx)
	if err != nil {
		return fmt.Errorf("load company settings: %w", err)
	}
	if used < cs.NextInvoiceNumber {
		return nil
	}
	_, err = s.client.CompanySettings.UpdateOneID(cs.ID).SetNextInvoiceNumber(used + 1).Save(ctx)
	if err != nil {
		return fmt.Errorf("update next invoice number: %w", err)
	}
	return nil
}

func FormatInvoiceNumber(invoiceNumber int64, cs *ent.CompanySettings) string {
	if cs == nil {
		return fmt.Sprintf("INV-%05d", invoiceNumber)
	}
	prefix := strings.TrimSpace(cs.InvoicePrefix)
	return fmt.Sprintf("%s%05d", prefix, invoiceNumber)
}

func FormatEstimateNumber(estimateID int64, cs *ent.CompanySettings) string {
	if cs == nil {
		return fmt.Sprintf("EST-%05d", estimateID)
	}
	prefix := strings.TrimSpace(cs.EstimatePrefix)
	return fmt.Sprintf("%s%05d", prefix, estimateID)
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
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateDocumentStatus(ctx, s.client, statusID, current.CompanyID, "invoice"); err != nil {
		return nil, err
	}
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
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := validateDocumentStatus(ctx, s.client, statusID, current.CompanyID, "invoice"); err != nil {
		return err
	}
	if _, err := s.client.Invoice.UpdateOneID(id).SetStatusID(statusID).Save(ctx); err != nil {
		return fmt.Errorf("set invoice status %d: %w", id, err)
	}
	return nil
}

func InvoicePaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

func (s *InvoiceService) LineItems(i *ent.Invoice) []LineItem {
	items, _ := DecodeLineItems(i.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

func (s *InvoiceService) CreateFromJob(ctx context.Context, jobID int64, statusSvc *StatusService, defaultTaxRate string) (*ent.Invoice, error) {
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	newStatus, _ := statusSvc.DraftForObjectType(ctx, "invoice")
	var statusID int64
	if newStatus != nil {
		statusID = newStatus.ID
	}

	items, err := DecodeLineItems(j.LineItems)
	if err != nil {
		return nil, fmt.Errorf("parse job %d line items: %w", jobID, err)
	}
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

func InvoiceTotal(items []LineItem, taxRate string) (float64, error) {
	totals, err := CalculateTotals(items, taxRate)
	if err != nil {
		return 0, err
	}
	return totals.Total.MajorUnits(), nil
}
