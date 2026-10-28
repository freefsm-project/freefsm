package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNegativeInvoiceTotal = errors.New("invoice total must not be negative")
var ErrInvoiceArchiveIneligible = errors.New("invoice can only be archived when paid or void")

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
	Adjustments   DocumentAdjustments
	Terms         string
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
	Adjustments              *DocumentAdjustments
	CustomerDefaultsReviewed bool
	Terms                    *string
	InvoiceNumber            *int64
	CustomerID               *int64
	JobID                    *int64
	EstimateID               *int64
	Title                    *string
	Notes                    *string
	InvoiceDate              *time.Time
	DueDate                  *time.Time
	TaxRate                  *string
	LineItems                *[]LineItem
	CustomFields             *string
}

func (s *InvoiceService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Invoice, int, error) {
	q := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil(), invoice.ConversionHiddenAtIsNil())

	if search != "" {
		q = q.Where(invoice.TitleContainsFold(search))
	}

	if statusID > 0 {
		ids, filterErr := s.effectiveStatusInvoiceIDs(ctx, statusID)
		if filterErr != nil {
			return nil, 0, filterErr
		}
		q = q.Where(invoice.IDIn(ids...))
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
	if s.db != nil && len(latest) > 0 {
		ids := make([]int64, 0, len(latest))
		byID := make(map[int64]*ent.Invoice, len(latest))
		for _, inv := range latest {
			ids = append(ids, inv.ID)
			byID[inv.ID] = inv
		}
		rows, queryErr := s.db.Query(ctx, `SELECT invoice_id,status_id FROM invoice_effective_status WHERE invoice_id=ANY($1)`, ids)
		if queryErr != nil {
			return nil, fmt.Errorf("load effective invoice statuses: %w", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var invoiceID, statusID int64
			if queryErr = rows.Scan(&invoiceID, &statusID); queryErr != nil {
				return nil, queryErr
			}
			if inv := byID[invoiceID]; inv != nil {
				inv.StatusID = &statusID
			}
		}
		if queryErr = rows.Err(); queryErr != nil {
			return nil, queryErr
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
		ids, filterErr := s.effectiveStatusInvoiceIDs(ctx, statusID)
		if filterErr != nil {
			return nil, 0, filterErr
		}
		q = q.Where(invoice.IDIn(ids...))
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

func (s *InvoiceService) effectiveStatusInvoiceIDs(ctx context.Context, statusID int64) ([]int64, error) {
	if s.db == nil {
		return nil, errors.New("effective invoice status filtering requires PostgreSQL")
	}
	rows, err := s.db.Query(ctx, `SELECT invoice_id FROM invoice_effective_status WHERE status_id=$1`, statusID)
	if err != nil {
		return nil, fmt.Errorf("filter effective invoice status: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []int64{-1}, nil
	}
	return ids, nil
}

func (s *InvoiceService) GetByID(ctx context.Context, id int64) (*ent.Invoice, error) {
	i, err := s.client.Invoice.Query().Where(invoice.IDEQ(id), invoice.ConversionHiddenAtIsNil()).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get invoice %d: %w", id, err)
	}
	return i, nil
}

func (s *InvoiceService) Create(ctx context.Context, params InvoiceCreateParams) (*ent.Invoice, error) {
	if needsCommercialTransaction(ctx) {
		return commercialWrite(ctx, s.client, func(ctx context.Context, c *ent.Client) (*ent.Invoice, error) {
			return NewInvoiceService(c).Create(ctx, params)
		})
	}
	if err := commercialWriteCustomer(ctx, "invoice", 0, params.CustomerID); err != nil {
		return nil, err
	}
	if needsDetailsTransaction(ctx) {
		return saveDetails(ctx, "invoice", 0, params.JobID, &params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Invoice, int64, error) {
			params.CustomFields = fields
			v, err := NewInvoiceService(c).Create(ctx, params)
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
	}
	taxRate := params.TaxRate
	if strings.TrimSpace(taxRate) == "" {
		taxRate = "0"
	}
	financial, err := AcceptFinancial(ctx, s.client, params.CustomerID, params.LineItems, taxRate, params.Adjustments)
	if err != nil {
		return nil, err
	}
	totals, err := FinancialTotals(financial, "", taxRate)
	if err != nil {
		return nil, fmt.Errorf("calculate invoice totals: %w", err)
	}
	if totals.Total.MinorUnits() < 0 {
		return nil, ErrNegativeInvoiceTotal
	}
	lineItems, err := EncodeCommercialLines(params.LineItems)
	if err != nil {
		return nil, fmt.Errorf("encode invoice line items: %w", err)
	}
	if err := validateJobCustomer(ctx, s.client, params.CustomerID, params.JobID, true); err != nil {
		return nil, err
	}
	if err := validateEstimateCustomer(ctx, s.client, params.CustomerID, params.EstimateID, true); err != nil {
		return nil, err
	}
	if customerCompanyID == nil {
		customer, err := s.client.Customer.Get(ctx, params.CustomerID)
		if err != nil {
			return nil, fmt.Errorf("get invoice customer: %w", err)
		}
		customerCompanyID = customer.CompanyID
	}
	statusID, err := creationStatus(ctx, s.client, 0, customerCompanyID, "invoice", "invoice:draft")
	if err != nil {
		return nil, err
	}
	params.StatusID = statusID

	customFields := params.CustomFields
	if customFields == "" {
		customFields = "[]"
	}
	if request, ok := authorization.FromContext(ctx); ok {
		id, err := createDocument(ctx, request, "invoice", authorization.DocumentCreation{Financial: financial, CustomerID: params.CustomerID, JobID: params.JobID, EstimateID: params.EstimateID, Title: params.Title, Notes: params.Notes, Terms: params.Terms, TaxRate: taxRate, LineItems: lineItems, CustomFields: customFields, InvoiceDate: params.InvoiceDate, DueDate: params.DueDate, Number: params.InvoiceNumber}, func(rate string) (string, error) {
			totals, err := FinancialTotals(financial, "", rate)
			if err != nil {
				return "", err
			}
			if totals.Total.MinorUnits() < 0 {
				return "", ErrNegativeInvoiceTotal
			}
			return invoiceSettlementState(totals.Total.MinorUnits(), 0), nil
		})
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, id)
	}
	b := s.client.Invoice.Create().
		SetNillableCompanyID(customerCompanyID).
		SetCustomerID(params.CustomerID).
		SetSettlementState(invoiceSettlementState(totals.Total.MinorUnits(), 0)).
		SetTitle(params.Title).
		SetNotes(params.Notes).
		SetTerms(params.Terms).
		SetInvoiceDate(params.InvoiceDate).
		SetDueDate(params.DueDate).
		SetTaxRate(taxRate).
		SetLineItems(lineItems).
		SetFinancial(financial).
		SetCustomFields(customFields)

	if params.JobID > 0 {
		b.SetJobID(params.JobID)
	}
	if params.EstimateID > 0 {
		b.SetEstimateID(params.EstimateID)
	}
	b.SetStatusID(statusID)
	if err := s.assignInvoiceNumber(ctx, b, params.InvoiceNumber); err != nil {
		return nil, err
	}

	i, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	return i, nil
}

func (s *InvoiceService) Update(ctx context.Context, id int64, params InvoiceUpdateParams) (*ent.Invoice, error) {
	if _, ok := authorization.FromContext(ctx); ok {
		return s.SaveDetails(ctx, id, params)
	}
	if needsCommercialTransaction(ctx) {
		return commercialWrite(ctx, s.client, func(ctx context.Context, c *ent.Client) (*ent.Invoice, error) {
			return NewInvoiceService(c).Update(ctx, id, params)
		})
	}
	if err := commercialWriteCustomer(ctx, "invoice", id, int64Value(params.CustomerID)); err != nil {
		return nil, err
	}
	return s.update(ctx, id, params)
}

func (s *InvoiceService) SaveDetails(ctx context.Context, id int64, params InvoiceUpdateParams) (*ent.Invoice, error) {
	return saveDetails(ctx, "invoice", id, 0, params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Invoice, int64, error) {
		params.CustomFields = &fields
		v, err := NewInvoiceService(c).update(ctx, id, params)
		return v, id, err
	})
}

func (s *InvoiceService) update(ctx context.Context, id int64, params InvoiceUpdateParams) (*ent.Invoice, error) {
	if params.TaxRate != nil && strings.TrimSpace(*params.TaxRate) == "" {
		zero := "0"
		params.TaxRate = &zero
	}
	if params.Title != nil && strings.TrimSpace(*params.Title) == "" {
		return nil, authorization.ErrCommercialInput
	}
	if params.InvoiceDate != nil && params.InvoiceDate.IsZero() {
		return nil, authorization.ErrCommercialInput
	}
	if needsCatalogTransaction(ctx) && params.LineItems != nil {
		return catalogWrite(ctx, s.client, "invoice", id, *params.LineItems, func(ctx context.Context, c *ent.Client) (*ent.Invoice, error) {
			return NewInvoiceService(c).update(ctx, id, params)
		})
	}
	var encodedLineItems string
	if params.LineItems != nil {
		var err error
		encodedLineItems, err = EncodeCommercialLines(*params.LineItems)
		if err != nil {
			return nil, fmt.Errorf("encode invoice line items: %w", err)
		}
	}
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var updatedTotalCents int64
	financial := current.Financial
	monetaryUpdate := MonetaryChanged(current.LineItems, current.TaxRate, current.Financial, params.LineItems, params.TaxRate, params.Adjustments) || params.CustomerID != nil && *params.CustomerID != current.CustomerID
	if monetaryUpdate {
		if request, ok := authorization.FromContext(ctx); ok {
			if err := request.Service.ParentFacet(ctx, request.Actor, "invoice", id, "pricing.view"); err != nil {
				return nil, err
			}
			customer, job := current.CustomerID, int64Value(current.JobID)
			if params.CustomerID != nil {
				customer = *params.CustomerID
			}
			if params.JobID != nil {
				job = *params.JobID
			}
			if customer != current.CustomerID && job == int64Value(current.JobID) {
				job = 0
			}
			if _, err := request.Service.DocumentCustomer(ctx, request.Actor, customer, job, current.TaxRate); err != nil {
				return nil, err
			}
		}
		items := []LineItem(nil)
		if params.LineItems != nil {
			items = *params.LineItems
		} else {
			items, err = DecodeCommercialLines(current.LineItems)
			if err != nil {
				return nil, fmt.Errorf("decode invoice line items: %w", err)
			}
		}
		taxRate := current.TaxRate
		if params.TaxRate != nil {
			taxRate = *params.TaxRate
		}
		adjustments, err := FinancialAdjustments(current.Financial)
		if err != nil {
			return nil, err
		}
		if params.Adjustments != nil {
			adjustments = *params.Adjustments
		}
		customer := current.CustomerID
		if params.CustomerID != nil {
			customer = *params.CustomerID
		}
		financial, err = AcceptFinancial(ctx, s.client, customer, items, taxRate, adjustments)
		if err != nil {
			return nil, err
		}
		totals, err := FinancialTotals(financial, "", taxRate)
		if err != nil {
			return nil, fmt.Errorf("calculate invoice totals: %w", err)
		}
		if totals.Total.MinorUnits() < 0 {
			return nil, ErrNegativeInvoiceTotal
		}
		updatedTotalCents = totals.Total.MinorUnits()
	}
	customerID := current.CustomerID
	if !monetaryUpdate {
		params.TaxRate = nil
	}
	if !monetaryUpdate && params.LineItems != nil {
		encodedLineItems, err = preserveCapturedLineMoney(current.LineItems, *params.LineItems)
		if err != nil {
			return nil, err
		}
	}
	jobID := int64Value(current.JobID)
	estimateID := int64Value(current.EstimateID)
	if params.CustomerID != nil {
		customerID = *params.CustomerID
	}
	if customerID != current.CustomerID && !params.CustomerDefaultsReviewed {
		return nil, authorization.ErrConflict
	}
	if params.JobID != nil {
		jobID = *params.JobID
	}
	if params.EstimateID != nil {
		estimateID = *params.EstimateID
	}
	if customerID != current.CustomerID && jobID == int64Value(current.JobID) {
		jobID = 0
		params.JobID = &jobID
	}
	if err := validateJobCustomer(ctx, s.client, customerID, jobID, params.JobID != nil && jobID != int64Value(current.JobID)); err != nil {
		return nil, err
	}
	if params.EstimateID != nil && estimateID != int64Value(current.EstimateID) {
		// Source provenance is not an editable commercial anchor.
		return nil, authorization.ErrConflict
	}

	u := s.client.Invoice.UpdateOneID(id)
	if monetaryUpdate {
		u.SetFinancial(financial)
	}
	if params.Terms != nil {
		u.SetTerms(*params.Terms)
	}
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
	if monetaryUpdate {
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
	// reserve_document_identity owns the high-water mark and issued identity in
	// the insert transaction. Advancing it first both rejects this number and,
	// in the old context-free adapter, could leave a partial numbering effect.
	return nil
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

func RecordedInvoiceNumber(i *ent.Invoice, cs *ent.CompanySettings) string {
	if i.NumberLabel != "" {
		return i.NumberLabel
	}
	return fmt.Sprintf("Invoice record %d — identity requires review", i.ID)
}

func RecordedEstimateNumber(e *ent.Estimate, cs *ent.CompanySettings) string {
	if e.NumberLabel != "" {
		return e.NumberLabel
	}
	return fmt.Sprintf("Estimate record %d — identity requires review", e.ID)
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
		// Settlement may change after the page/handler check. The database
		// rechecks under the shared fence; expose that decision as a conflict,
		// not an internal error or raw PostgreSQL diagnostic.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "P0001" && pgErr.Message == "invoice archive requires paid or void" {
			return ErrInvoiceArchiveIneligible
		}
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
	if needsCommercialTransaction(ctx) {
		return commercialWrite(ctx, s.client, func(ctx context.Context, c *ent.Client) (*ent.Invoice, error) {
			return NewInvoiceService(c).CreateFromJob(ctx, jobID, statusSvc, defaultTaxRate)
		})
	}
	ctx = authorization.WithCapturedItemSource(ctx, "job", jobID)
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", jobID, err)
	}

	items, err := DecodeCommercialLines(j.LineItems)
	if err != nil {
		return nil, fmt.Errorf("parse job %d line items: %w", jobID, err)
	}
	cs, err := s.client.CompanySettings.Query().First(ctx)
	if err != nil {
		return nil, err
	}
	var customerDefaults map[string]*string
	if request, ok := authorization.FromContext(ctx); ok {
		customerDefaults, err = request.Service.DocumentDefaultInputs(ctx, request.Actor, "invoice", j.CustomerID, j.ID)
		if err != nil {
			return nil, err
		}
	}
	defaults := ResolveDocumentDefaults(cs, "invoice", customerDefaults)
	now, _ := time.Parse("2006-01-02", time.Now().In(companySettingsLocation(cs)).Format("2006-01-02"))
	due := time.Time{}
	if defaults.Days != nil {
		due = now.AddDate(0, 0, *defaults.Days)
	}
	notes := j.Notes
	if notes == "" {
		notes = defaults.Notes
	}
	return s.Create(ctx, InvoiceCreateParams{
		CustomerID:   j.CustomerID,
		JobID:        j.ID,
		StatusID:     0,
		Title:        j.JobType,
		Notes:        notes,
		Terms:        defaults.Terms,
		InvoiceDate:  now,
		DueDate:      due,
		TaxRate:      defaultTaxRate,
		CustomFields: "[]",
		LineItems:    items,
	})
}

// InvoiceTotal is the unversioned legacy helper; document reads use FinancialTotals.
func InvoiceTotal(items []LineItem, taxRate string) (float64, error) {
	totals, err := CalculateTotals(items, taxRate)
	if err != nil {
		return 0, err
	}
	return totals.Total.MajorUnits(), nil
}
