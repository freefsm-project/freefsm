package templates

import "net/url"

// URLs must be explicit authorized destinations, never inferred from identities.
type FinancialCell struct {
	Text, URL string
	Unknown   bool
}

type FinancialRow struct {
	Cells       []FinancialCell
	Allocations []FinancialSourcePortion
}

type FinancialSourcePortion struct {
	CreditID, PaymentID, SourceDate, Amount string
	Kind, ID, InvoiceID                     string
}

type FinancialSection struct {
	Title, Description string
	Headers            []string
	Rows               []FinancialRow
	Measures           []WorkReportMeasure
}

// Failed snapshots carry Error and never render sections or fabricated totals.
type FinancialReportPageData struct {
	Title, Path, Description, UpdatedAt, Timezone, Error string
	View                                                 string
	Filters                                              url.Values
	Views                                                []WorkReportOption
	Fields                                               []WorkReportField
	Sections                                             []FinancialSection
	Exports                                              []FinancialCell
}

type FinancialExportReviewData struct {
	Title, Path, Description, BackURL, Operation, Error string
	Filters                                             url.Values
	Columns                                             []string
}
