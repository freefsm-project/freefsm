package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrFinancialReportFilter = errors.New("invalid financial report filters")

type FinancialReportFilter struct {
	CustomerID      int64  `json:"customer_id,omitempty"`
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Sort            string `json:"sort,omitempty"`
}

var FinancialAgingBuckets = [6]string{"not_yet_due", "1_30", "31_60", "61_90", "91_plus", "no_due_date"}

type FinancialReport struct {
	Kind            authorization.FinancialReportKind
	Filter          FinancialReportFilter
	AsOf            time.Time
	Timezone        string
	Invoices        []FinancialInvoiceRow
	Customers       []FinancialCustomerRow
	Activity        []FinancialActivityRow
	UnknownActivity []FinancialActivityRow
	Credits         []FinancialCreditRow
	Totals          FinancialReportTotals
	References      []authorization.FinancialReference
}
type FinancialReportTotals struct {
	TotalCents              int64
	SettledCents            int64
	BucketTotals            [6]FinancialAgingTotal
	ReceivableCents         int64
	InvoiceCount            int
	Buckets                 [6]int64
	NetPaymentsCents        int64
	AvailableCreditCents    int64
	UnknownEventCount       int
	UnknownAmountCents      int64
	UnknownNetPaymentsCents int64
}
type FinancialAgingTotal struct {
	InvoiceCount                              int
	TotalCents, SettledCents, ReceivableCents int64
}
type FinancialInvoiceRow struct {
	InvoiceNumber                      int64
	ID, JobID, CustomerID              int64
	Customer, Number, Title, DueDate   string
	TotalCents, SettledCents, DueCents int64
	Bucket                             string
	Reference                          authorization.FinancialReference
}
type FinancialCustomerRow struct {
	TotalCents, SettledCents int64
	ID                       int64
	Name                     string
	Archived                 bool
	InvoiceCount             int
	ReceivableCents          int64
	Buckets                  [6]int64
	Reference                authorization.FinancialReference
}
type FinancialSourceAllocation struct {
	Kind, ID                        string
	InvoiceID                       int64
	CreditID, PaymentID, SourceDate string
	AmountCents                     int64
}
type FinancialActivityRow struct {
	Kind, ID, OriginalKind, OriginalID string
	CustomerID                         int64
	Customer                           string
	Archived                           bool
	PaymentID, CreditID                string
	InvoiceID                          int64
	RecordedAt                         *time.Time
	EffectiveDate                      string
	AmountCents, NetPaymentsCents      int64
	Allocations                        []FinancialSourceAllocation
	Reference                          authorization.FinancialReference
}
type FinancialCreditRow struct {
	RecordedAt                    *time.Time
	ID, PaymentID                 string
	CustomerID                    int64
	Customer                      string
	Archived                      bool
	SourceDate                    string
	OriginalCents, AvailableCents int64
	Reference                     authorization.FinancialReference
}

func NormalizeFinancialReportFilter(kind authorization.FinancialReportKind, f FinancialReportFilter) (FinancialReportFilter, error) {
	order := ""
	switch kind {
	case authorization.FinancialAging:
		order = "customer_invoice"
	case authorization.FinancialCustomers:
		order = "customer"
	case authorization.FinancialSettlement, authorization.FinancialCombined:
		order = "entry_asc"
	case authorization.FinancialCredit:
		order = "customer_source"
	default:
		return f, ErrFinancialReportFilter
	}
	if f.Sort == "" {
		f.Sort = order
	}
	if f.CustomerID < 0 || f.Sort != order {
		return f, ErrFinancialReportFilter
	}
	if f.IncludeArchived && (kind == authorization.FinancialAging || kind == authorization.FinancialCustomers) {
		return f, ErrFinancialReportFilter
	}
	if f.Bucket != "" {
		found := false
		for _, bucket := range FinancialAgingBuckets {
			if bucket == f.Bucket {
				found = true
			}
		}
		if kind != authorization.FinancialAging || !found {
			return f, ErrFinancialReportFilter
		}
	}
	if (f.From != "" || f.To != "") && kind != authorization.FinancialSettlement && kind != authorization.FinancialCombined {
		return f, ErrFinancialReportFilter
	}
	for _, date := range []string{f.From, f.To} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return f, ErrFinancialReportFilter
			}
		}
	}
	if f.From != "" && f.To != "" && f.From > f.To {
		return f, ErrFinancialReportFilter
	}
	return f, nil
}

func ReadFinancialReport(ctx context.Context, pool *pgxpool.Pool, a authorization.Actor, kind authorization.FinancialReportKind, f FinancialReportFilter) (FinancialReport, error) {
	if a.Channel == "api" {
		return FinancialReport{}, authorization.ErrForbidden
	}
	var out FinancialReport
	err := authorization.New(pool).WithinSnapshotPermission(ctx, a, authorization.FinancialReportPermission(kind, false), authorization.All, func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		out, err = FinancialReportSnapshot(ctx, tx, p, kind, f)
		return err
	})
	if err != nil {
		return FinancialReport{}, err
	}
	return out, nil
}

// FinancialReportSnapshot never reaches outside the caller's transaction. No
// partial report is returned on query, arithmetic, authorization or iteration error.
func FinancialReportSnapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, kind authorization.FinancialReportKind, f FinancialReportFilter) (FinancialReport, error) {
	f, err := NormalizeFinancialReportFilter(kind, f)
	if err != nil {
		return FinancialReport{}, err
	}
	if err := authorization.CheckFinancialSelection(ctx, tx, p, kind, f.CustomerID, f.IncludeArchived, false); err != nil {
		return FinancialReport{}, err
	}
	out := FinancialReport{Kind: kind, Filter: f}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp(),timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&out.AsOf, &out.Timezone); err != nil {
		return FinancialReport{}, err
	}
	loc, err := time.LoadLocation(out.Timezone)
	if err != nil {
		return FinancialReport{}, err
	}
	switch kind {
	case authorization.FinancialAging, authorization.FinancialCustomers:
		err = readFinancialReceivables(ctx, tx, p, &out, loc)
	case authorization.FinancialSettlement:
		err = readFinancialActivity(ctx, tx, p, &out, loc)
	case authorization.FinancialCredit:
		err = readFinancialCredit(ctx, tx, p, &out)
	case authorization.FinancialCombined:
		err = readFinancialActivity(ctx, tx, p, &out, loc)
		if err == nil {
			err = readFinancialCredit(ctx, tx, p, &out)
		}
	}
	if err != nil {
		return FinancialReport{}, err
	}
	return out, nil
}

func financialAdd(target *int64, amount int64) error {
	if amount > 0 && *target > math.MaxInt64-amount || amount < 0 && *target < math.MinInt64-amount {
		return fmt.Errorf("financial report total exceeds exact cents range")
	}
	*target += amount
	return nil
}

func financialSourceDate(date string) string {
	if date == "0001-01-01" {
		return ""
	}
	return date
}

func financialDateBounds(f FinancialReportFilter, loc *time.Location) (*time.Time, *time.Time, error) {
	var from, until *time.Time
	if f.From != "" {
		t, err := time.ParseInLocation(time.DateOnly, f.From, loc)
		if err != nil {
			return nil, nil, err
		}
		from = &t
	}
	if f.To != "" {
		t, err := time.ParseInLocation(time.DateOnly, f.To, loc)
		if err != nil {
			return nil, nil, err
		}
		t = t.AddDate(0, 0, 1)
		until = &t
	}
	return from, until, nil
}
