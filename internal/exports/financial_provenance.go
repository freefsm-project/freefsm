package exports

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

// These retained identity tuples supplement the core's current facet/Customer
// checks. They do not enrich Invoice details or re-evaluate monetary snapshots.
type financialProvenance struct {
	Facet        authorization.FinancialReportKind `json:"facet"`
	CustomerID   int64                             `json:"customer_id"`
	Kind         string                            `json:"kind"`
	ID           string                            `json:"id"`
	OriginalKind string                            `json:"original_kind,omitempty"`
	OriginalID   string                            `json:"original_id,omitempty"`
	PaymentID    string                            `json:"payment_id,omitempty"`
	CreditID     string                            `json:"credit_id,omitempty"`
	InvoiceID    int64                             `json:"invoice_id,omitempty"`
	Allocations  []financialAllocationIdentity     `json:"allocations,omitempty"`
}

type financialAllocationIdentity struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	PaymentID string `json:"payment_id"`
	CreditID  string `json:"credit_id,omitempty"`
	InvoiceID int64  `json:"invoice_id,omitempty"`
}

func financialEventProvenance(event services.FinancialActivityRow) financialProvenance {
	p := financialProvenance{Facet: authorization.FinancialSettlement, CustomerID: event.CustomerID, Kind: event.Kind, ID: event.ID, OriginalKind: event.OriginalKind, OriginalID: event.OriginalID, PaymentID: event.PaymentID, CreditID: event.CreditID, InvoiceID: event.InvoiceID}
	for _, a := range event.Allocations {
		p.Allocations = append(p.Allocations, financialAllocationIdentity{Kind: a.Kind, ID: a.ID, PaymentID: a.PaymentID, CreditID: a.CreditID, InvoiceID: a.InvoiceID})
	}
	return p
}

func financialReportProvenance(report services.FinancialReport) []financialProvenance {
	var result []financialProvenance
	for _, event := range report.Activity {
		result = append(result, financialEventProvenance(event))
	}
	for _, event := range report.UnknownActivity {
		result = append(result, financialEventProvenance(event))
	}
	for _, credit := range report.Credits {
		result = append(result, financialProvenance{Facet: authorization.FinancialCredit, CustomerID: credit.CustomerID, Kind: "credit_source", ID: credit.ID, CreditID: credit.ID, PaymentID: credit.PaymentID})
	}
	return result
}

func (source financialProvenance) validate(kind authorization.FinancialReportKind) error {
	if source.CustomerID <= 0 || !financialUUID(source.ID) || source.InvoiceID < 0 {
		return authorization.ErrForbidden
	}
	for _, id := range []string{source.PaymentID, source.CreditID, source.OriginalID} {
		if id != "" && !financialUUID(id) {
			return authorization.ErrForbidden
		}
	}
	if source.Facet != kind && !(kind == authorization.FinancialCombined && (source.Facet == authorization.FinancialCredit || source.Facet == authorization.FinancialSettlement)) {
		return authorization.ErrForbidden
	}
	if source.Facet == authorization.FinancialCredit {
		if source.Kind != "credit_source" || source.CreditID != source.ID || source.PaymentID == "" || source.OriginalKind != "" || source.OriginalID != "" || source.InvoiceID != 0 || len(source.Allocations) != 0 {
			return authorization.ErrForbidden
		}
		return nil
	}
	if source.Facet != authorization.FinancialSettlement {
		return authorization.ErrForbidden
	}
	if _, ok := financialEventType(source.Kind); !ok {
		return authorization.ErrForbidden
	}
	wantOriginal := ""
	switch source.Kind {
	case "payment_reversal":
		wantOriginal = "payment"
	case "application_reversal":
		wantOriginal = "credit_application"
	case "refund_reversal":
		wantOriginal = "credit_refund"
	}
	if source.OriginalKind != wantOriginal || (source.OriginalID != "") != (wantOriginal != "") {
		return authorization.ErrForbidden
	}
	baseKind, baseID := source.Kind, source.ID
	if wantOriginal != "" {
		baseKind, baseID = wantOriginal, source.OriginalID
	}
	switch baseKind {
	case "payment":
		if source.PaymentID != baseID || source.CreditID != "" {
			return authorization.ErrForbidden
		}
	case "credit_creation":
		if source.CreditID != baseID || source.PaymentID == "" || len(source.Allocations) != 0 {
			return authorization.ErrForbidden
		}
	case "credit_application":
		if source.PaymentID == "" || source.CreditID == "" || source.InvoiceID == 0 {
			return authorization.ErrForbidden
		}
	case "credit_refund":
		if source.PaymentID != "" || source.CreditID != "" || source.InvoiceID != 0 {
			return authorization.ErrForbidden
		}
	}
	for _, a := range source.Allocations {
		if a.ID != baseID {
			return authorization.ErrForbidden
		}
		if !financialUUID(a.ID) || !financialUUID(a.PaymentID) || a.InvoiceID < 0 {
			return authorization.ErrForbidden
		}
		if a.CreditID != "" && !financialUUID(a.CreditID) {
			return authorization.ErrForbidden
		}
		switch a.Kind {
		case "payment_invoice":
			if baseKind != "payment" || a.PaymentID != source.PaymentID || a.InvoiceID != source.InvoiceID || a.CreditID != "" || a.InvoiceID == 0 {
				return authorization.ErrForbidden
			}
		case "credit_application":
			if baseKind != "credit_application" || a.PaymentID != source.PaymentID || a.CreditID != source.CreditID || a.InvoiceID != source.InvoiceID || a.CreditID == "" || a.InvoiceID == 0 {
				return authorization.ErrForbidden
			}
		case "refund_credit":
			if baseKind != "credit_refund" || a.CreditID == "" || a.InvoiceID != 0 {
				return authorization.ErrForbidden
			}
		default:
			return authorization.ErrForbidden
		}
	}
	return nil
}

func (source financialProvenance) recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal) error {
	kind, id := source.Kind, source.ID
	if source.OriginalID != "" {
		operation := map[string]string{"payment": "payment", "credit_application": "credit_application", "credit_refund": "credit_refund"}[source.OriginalKind]
		if err := financialProvenanceExists(ctx, tx, `SELECT EXISTS(SELECT 1 FROM settlement_reversals WHERE company_id=$1 AND id::text=$2 AND operation_type=$3 AND operation_id::text=$4)`, p.CompanyID, source.ID, operation, source.OriginalID); err != nil {
			return err
		}
		kind, id = source.OriginalKind, source.OriginalID
	}
	var query string
	switch kind {
	case "payment":
		query = `SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE company_id=$1 AND customer_id=$2 AND id::text=$3 AND id::text=$4 AND $5::text='' AND coalesce(invoice_id,0)=$6)`
	case "credit_creation", "credit_source":
		query = `SELECT EXISTS(SELECT 1 FROM customer_credits c JOIN invoice_payments p ON p.id=c.source_payment_id AND p.company_id=c.company_id AND p.customer_id=c.customer_id WHERE c.company_id=$1 AND c.customer_id=$2 AND c.id::text=$3 AND c.source_payment_id::text=$4 AND c.id::text=$5 AND ($7 OR coalesce(p.invoice_id,0)=$6))`
	case "credit_application":
		query = `SELECT EXISTS(SELECT 1 FROM credit_applications a JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=a.company_id AND c.customer_id=a.customer_id WHERE a.company_id=$1 AND a.customer_id=$2 AND a.id::text=$3 AND c.source_payment_id::text=$4 AND a.credit_id::text=$5 AND a.invoice_id=$6)`
	case "credit_refund":
		query = `SELECT EXISTS(SELECT 1 FROM credit_refunds WHERE company_id=$1 AND customer_id=$2 AND id::text=$3 AND $4::text='' AND $5::text='' AND $6::bigint=0)`
	default:
		return authorization.ErrForbidden
	}
	args := []any{p.CompanyID, source.CustomerID, id, source.PaymentID, source.CreditID, source.InvoiceID}
	if kind == "credit_creation" || kind == "credit_source" {
		args = append(args, kind == "credit_source")
	}
	if err := financialProvenanceExists(ctx, tx, query, args...); err != nil {
		return err
	}
	for _, a := range source.Allocations {
		args := []any{p.CompanyID, source.CustomerID, a.ID, a.PaymentID, a.CreditID, a.InvoiceID}
		switch a.Kind {
		case "payment_invoice":
			query = `SELECT EXISTS(SELECT 1 FROM payment_invoice_allocations a JOIN invoice_payments p ON p.id=a.payment_id WHERE p.company_id=$1 AND p.customer_id=$2 AND a.payment_id::text=$3 AND a.payment_id::text=$4 AND $5::text='' AND a.invoice_id=$6)`
		case "credit_application":
			query = `SELECT EXISTS(SELECT 1 FROM credit_applications a JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=a.company_id AND c.customer_id=a.customer_id WHERE a.company_id=$1 AND a.customer_id=$2 AND a.id::text=$3 AND c.source_payment_id::text=$4 AND a.credit_id::text=$5 AND a.invoice_id=$6)`
		case "refund_credit":
			query = `SELECT EXISTS(SELECT 1 FROM credit_refund_allocations a JOIN credit_refunds f ON f.id=a.refund_id JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=f.company_id AND c.customer_id=f.customer_id WHERE f.company_id=$1 AND f.customer_id=$2 AND a.refund_id::text=$3 AND c.source_payment_id::text=$4 AND a.credit_id::text=$5 AND $6::bigint=0)`
		default:
			return authorization.ErrForbidden
		}
		if err := financialProvenanceExists(ctx, tx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

func financialProvenanceExists(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	var exists bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return authorization.ErrNotFound
	}
	return nil
}
