package exports

import (
	"context"
	"encoding/json"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

// These five zero-value definitions are fixed product outputs, not configurable
// field/facet selections. Product registers the same definitions for HTTP/worker.
type InvoiceAging struct{}
type CustomerFinancials struct{}
type SettlementActivity struct{}
type CurrentCredit struct{}
type PaymentsCredits struct{}

const (
	InvoiceAgingKind       = "invoice-aging-A-v1"
	CustomerFinancialsKind = "customer-financial-A-v1"
	SettlementActivityKind = "settlement-activity-A-v1"
	CurrentCreditKind      = "current-credit-A-v1"
	PaymentsCreditsKind    = "payments-credits-A-v1"
)

func (InvoiceAging) Kind() string                     { return InvoiceAgingKind }
func (CustomerFinancials) Kind() string               { return CustomerFinancialsKind }
func (SettlementActivity) Kind() string               { return SettlementActivityKind }
func (CurrentCredit) Kind() string                    { return CurrentCreditKind }
func (PaymentsCredits) Kind() string                  { return PaymentsCreditsKind }
func (InvoiceAging) Permission() string               { return "customer.financial.export" }
func (CustomerFinancials) Permission() string         { return "customer.financial.export" }
func (SettlementActivity) Permission() string         { return "settlement.export" }
func (CurrentCredit) Permission() string              { return "credit.export" }
func (PaymentsCredits) Permission() string            { return "settlement.export" }
func (InvoiceAging) Scope() authorization.Scope       { return authorization.All }
func (CustomerFinancials) Scope() authorization.Scope { return authorization.All }
func (SettlementActivity) Scope() authorization.Scope { return authorization.All }
func (CurrentCredit) Scope() authorization.Scope      { return authorization.All }
func (PaymentsCredits) Scope() authorization.Scope    { return authorization.All }
func (InvoiceAging) Columns() []string                { return financialAgingColumns() }
func (CustomerFinancials) Columns() []string          { return financialCustomerColumns() }
func (SettlementActivity) Columns() []string          { return financialSettlementColumns() }
func (CurrentCredit) Columns() []string               { return financialCreditColumns() }
func (PaymentsCredits) Columns() []string             { return financialCombinedColumns() }

func (InvoiceAging) Validate(raw json.RawMessage) error {
	return validateFinancialExport(authorization.FinancialAging, raw)
}
func (CustomerFinancials) Validate(raw json.RawMessage) error {
	return validateFinancialExport(authorization.FinancialCustomers, raw)
}
func (SettlementActivity) Validate(raw json.RawMessage) error {
	return validateFinancialExport(authorization.FinancialSettlement, raw)
}
func (CurrentCredit) Validate(raw json.RawMessage) error {
	return validateFinancialExport(authorization.FinancialCredit, raw)
}
func (PaymentsCredits) Validate(raw json.RawMessage) error {
	return validateFinancialExport(authorization.FinancialCombined, raw)
}

func (InvoiceAging) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeFinancialExport(p, authorization.FinancialAging, raw)
}
func (CustomerFinancials) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeFinancialExport(p, authorization.FinancialCustomers, raw)
}
func (SettlementActivity) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeFinancialExport(p, authorization.FinancialSettlement, raw)
}
func (CurrentCredit) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeFinancialExport(p, authorization.FinancialCredit, raw)
}
func (PaymentsCredits) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeFinancialExport(p, authorization.FinancialCombined, raw)
}

func (InvoiceAging) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return selectFinancialExport(ctx, tx, p, authorization.FinancialAging, raw)
}
func (CustomerFinancials) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return selectFinancialExport(ctx, tx, p, authorization.FinancialCustomers, raw)
}
func (SettlementActivity) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return selectFinancialExport(ctx, tx, p, authorization.FinancialSettlement, raw)
}
func (CurrentCredit) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return selectFinancialExport(ctx, tx, p, authorization.FinancialCredit, raw)
}
func (PaymentsCredits) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return selectFinancialExport(ctx, tx, p, authorization.FinancialCombined, raw)
}

func (InvoiceAging) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotFinancialExport(ctx, tx, p, authorization.FinancialAging, raw, emit)
}
func (CustomerFinancials) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotFinancialExport(ctx, tx, p, authorization.FinancialCustomers, raw, emit)
}
func (SettlementActivity) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotFinancialExport(ctx, tx, p, authorization.FinancialSettlement, raw, emit)
}
func (CurrentCredit) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotFinancialExport(ctx, tx, p, authorization.FinancialCredit, raw, emit)
}
func (PaymentsCredits) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotFinancialExport(ctx, tx, p, authorization.FinancialCombined, raw, emit)
}

func (InvoiceAging) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return recheckFinancialExport(ctx, tx, p, authorization.FinancialAging, raw)
}
func (CustomerFinancials) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return recheckFinancialExport(ctx, tx, p, authorization.FinancialCustomers, raw)
}
func (SettlementActivity) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return recheckFinancialExport(ctx, tx, p, authorization.FinancialSettlement, raw)
}
func (CurrentCredit) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return recheckFinancialExport(ctx, tx, p, authorization.FinancialCredit, raw)
}
func (PaymentsCredits) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return recheckFinancialExport(ctx, tx, p, authorization.FinancialCombined, raw)
}

func validateFinancialExport(kind authorization.FinancialReportKind, raw json.RawMessage) error {
	_, err := ParseFinancialExportFilter(kind, raw)
	return err
}

func authorizeFinancialExport(p authorization.Principal, kind authorization.FinancialReportKind, raw json.RawMessage) error {
	if err := validateFinancialExport(kind, raw); err != nil {
		return err
	}
	return financialExportGrants(p, kind)
}

func financialExportGrants(p authorization.Principal, kind authorization.FinancialReportKind) error {
	if err := authorization.AuthorizeFinancialReport(p, kind, true); err != nil {
		return err
	}
	// Invoice View is a fixed prerequisite even for an empty receivables CSV.
	if (kind == authorization.FinancialAging || kind == authorization.FinancialCustomers) && !p.Allows("invoice.view", authorization.All) && !p.Allows("invoice.view", authorization.Assigned) {
		return authorization.ErrForbidden
	}
	return nil
}

func selectFinancialExport(ctx context.Context, tx pgx.Tx, p authorization.Principal, kind authorization.FinancialReportKind, raw json.RawMessage) error {
	if err := authorizeFinancialExport(p, kind, raw); err != nil {
		return err
	}
	f, err := ParseFinancialExportFilter(kind, raw)
	if err != nil {
		return err
	}
	return authorization.CheckFinancialSelection(ctx, tx, p, kind, f.CustomerID, f.IncludeArchived, true)
}

func snapshotFinancialExport(ctx context.Context, tx pgx.Tx, p authorization.Principal, kind authorization.FinancialReportKind, raw json.RawMessage, emit func(Record) error) error {
	if err := selectFinancialExport(ctx, tx, p, kind, raw); err != nil {
		return err
	}
	f, err := ParseFinancialExportFilter(kind, raw)
	if err != nil {
		return err
	}
	report, err := services.FinancialReportSnapshot(ctx, tx, p, kind, f)
	if err != nil {
		return err
	}
	return emitFinancialReport(report, emit)
}
