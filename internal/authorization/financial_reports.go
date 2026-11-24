package authorization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type FinancialReportKind string

const (
	FinancialAging      FinancialReportKind = "invoice-aging"
	FinancialCustomers  FinancialReportKind = "customer-financial"
	FinancialSettlement FinancialReportKind = "settlement"
	FinancialCredit     FinancialReportKind = "credit"
	FinancialCombined   FinancialReportKind = "payments-credits"
)

func FinancialReportPermission(kind FinancialReportKind, exporting bool) string {
	stem := ""
	switch kind {
	case FinancialAging, FinancialCustomers:
		stem = "customer.financial"
	case FinancialSettlement, FinancialCombined:
		stem = "settlement"
	case FinancialCredit:
		stem = "credit"
	default:
		return ""
	}
	if exporting {
		return stem + ".export"
	}
	return stem + ".view"
}

// AuthorizeFinancialReport never infers one financial facet from another. The
// combined definition deliberately intersects the two independently useful ones.
func AuthorizeFinancialReport(p Principal, kind FinancialReportKind, exporting bool) error {
	permission := FinancialReportPermission(kind, false)
	if permission == "" || !p.Allows("customer.view", All) || !p.Allows("customer.financial.view", All) || !p.Allows(permission, All) {
		return ErrForbidden
	}
	if exporting && !p.Allows(FinancialReportPermission(kind, true), All) {
		return ErrForbidden
	}
	if (kind == FinancialAging || kind == FinancialCustomers) && !p.Allows("invoice.view", All) && !p.Allows("invoice.view", Assigned) {
		return ErrForbidden
	}
	if kind == FinancialCombined && (!p.Allows("credit.view", All) || exporting && !p.Allows("credit.export", All)) {
		return ErrForbidden
	}
	return nil
}

func CheckFinancialSelection(ctx context.Context, tx pgx.Tx, p Principal, kind FinancialReportKind, customerID int64, includeArchived, exporting bool) error {
	if err := AuthorizeFinancialReport(p, kind, exporting); err != nil {
		return err
	}
	if customerID < 0 {
		return ErrNotFound
	}
	if includeArchived && !p.Allows("customer.archived.view", All) {
		return ErrForbidden
	}
	if customerID == 0 {
		return nil
	}
	var archived bool
	err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM customers WHERE id=$1 AND company_id=$2`, customerID, p.CompanyID).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived && (!includeArchived || !p.Allows("customer.archived.view", All)) {
		return ErrNotFound
	}
	return nil
}

type FinancialInvoiceReference struct {
	ID       int64 `json:"id"`
	JobID    int64 `json:"job_id"`
	Archived bool  `json:"archived"`
}

// History identities are provenance, not ordinary document navigation. Invoices
// is populated only when Invoice financial amounts/labels actually contribute.
type FinancialReference struct {
	Facet      FinancialReportKind         `json:"facet"`
	CustomerID int64                       `json:"customer_id"`
	Archived   bool                        `json:"archived"`
	Invoices   []FinancialInvoiceReference `json:"invoices,omitempty"`
	EventKind  string                      `json:"event_kind,omitempty"`
	EventID    string                      `json:"event_id,omitempty"`
	CreditID   string                      `json:"credit_id,omitempty"`
}

func CheckFinancialReference(ctx context.Context, tx pgx.Tx, p Principal, ref FinancialReference) error {
	if err := AuthorizeFinancialReport(p, ref.Facet, false); err != nil {
		return err
	}
	if ref.CustomerID <= 0 {
		return ErrNotFound
	}
	if ref.Archived && !p.Allows("customer.archived.view", All) {
		return ErrForbidden
	}
	// Historical artifacts retain archived authority even after restoration;
	// currently archived Customers require it even if active at generation.
	var archived bool
	err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM customers WHERE company_id=$1 AND id=$2`, p.CompanyID, ref.CustomerID).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived && !p.Allows("customer.archived.view", All) {
		return ErrForbidden
	}
	for _, invoice := range ref.Invoices {
		if ref.Facet != FinancialAging && ref.Facet != FinancialCustomers {
			return ErrForbidden
		}
		if err := checkFinancialInvoice(ctx, tx, p, ref.CustomerID, invoice); err != nil {
			return err
		}
	}
	if ref.EventID != "" || ref.EventKind != "" {
		if ref.Facet != FinancialSettlement && ref.Facet != FinancialCombined {
			return ErrForbidden
		}
		if err := checkFinancialEvent(ctx, tx, p, ref.CustomerID, ref.EventKind, ref.EventID); err != nil {
			return err
		}
	}
	if ref.CreditID != "" {
		if ref.Facet != FinancialCredit && ref.Facet != FinancialCombined {
			return ErrForbidden
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customer_credits WHERE company_id=$1 AND customer_id=$2 AND id::text=$3)`, p.CompanyID, ref.CustomerID, ref.CreditID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

func checkFinancialInvoice(ctx context.Context, tx pgx.Tx, p Principal, customer int64, ref FinancialInvoiceReference) error {
	var job int64
	var archived, hidden bool
	err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0),deleted_at IS NOT NULL,conversion_hidden_at IS NOT NULL FROM invoices WHERE company_id=$1 AND customer_id=$2 AND id=$3`, p.CompanyID, customer, ref.ID).Scan(&job, &archived, &hidden)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if hidden || (archived || ref.Archived) && !p.Allows("invoice.archived.view", All) {
		return ErrForbidden
	}
	if p.Allows("invoice.view", All) {
		return nil
	}
	if !p.Allows("invoice.view", Assigned) || job == 0 || ref.JobID == 0 {
		return ErrForbidden
	}
	for _, id := range []int64{job, ref.JobID} {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND j.company_id=$2 AND j.customer_id=$3 AND j.deleted_at IS NULL AND a.user_id=$4)`, id, p.CompanyID, customer, p.UserID).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

func checkFinancialEvent(ctx context.Context, tx pgx.Tx, p Principal, customer int64, kind, id string) error {
	var query string
	switch kind {
	case "payment":
		query = `SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE company_id=$1 AND customer_id=$2 AND id::text=$3)`
	case "credit_creation":
		query = `SELECT EXISTS(SELECT 1 FROM customer_credits WHERE company_id=$1 AND customer_id=$2 AND id::text=$3)`
	case "credit_application":
		query = `SELECT EXISTS(SELECT 1 FROM credit_applications WHERE company_id=$1 AND customer_id=$2 AND id::text=$3)`
	case "credit_refund":
		query = `SELECT EXISTS(SELECT 1 FROM credit_refunds WHERE company_id=$1 AND customer_id=$2 AND id::text=$3)`
	case "payment_reversal":
		query = `SELECT EXISTS(SELECT 1 FROM settlement_reversals r JOIN invoice_payments p ON r.operation_type='payment' AND p.id=r.operation_id AND p.company_id=r.company_id WHERE r.company_id=$1 AND p.customer_id=$2 AND r.id::text=$3)`
	case "application_reversal":
		query = `SELECT EXISTS(SELECT 1 FROM settlement_reversals r JOIN credit_applications a ON r.operation_type='credit_application' AND a.id=r.operation_id AND a.company_id=r.company_id WHERE r.company_id=$1 AND a.customer_id=$2 AND r.id::text=$3)`
	case "refund_reversal":
		query = `SELECT EXISTS(SELECT 1 FROM settlement_reversals r JOIN credit_refunds f ON r.operation_type='credit_refund' AND f.id=r.operation_id AND f.company_id=r.company_id WHERE r.company_id=$1 AND f.customer_id=$2 AND r.id::text=$3)`
	default:
		return ErrForbidden
	}
	var ok bool
	if err := tx.QueryRow(ctx, query, p.CompanyID, customer, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
