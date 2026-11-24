package exports

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

// ParseFinancialExportFilter accepts only the fields of the selected fixed CSV.
// It returns the same canonical filter used by the report selectors.
func ParseFinancialExportFilter(kind authorization.FinancialReportKind, raw json.RawMessage) (services.FinancialReportFilter, error) {
	var f services.FinancialReportFilter
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return f, ErrConflict
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !financialFilterKey(kind, key) {
			return f, ErrConflict
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return f, ErrConflict
		}
		switch key {
		case "customer_id":
			err = json.Unmarshal(value, &f.CustomerID)
			if f.CustomerID <= 0 {
				return f, ErrConflict
			}
		case "from":
			err = json.Unmarshal(value, &f.From)
		case "to":
			err = json.Unmarshal(value, &f.To)
		case "bucket":
			err = json.Unmarshal(value, &f.Bucket)
		case "sort":
			err = json.Unmarshal(value, &f.Sort)
		case "include_archived":
			err = json.Unmarshal(value, &f.IncludeArchived)
		}
		if err != nil {
			return f, ErrConflict
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return f, ErrConflict
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return f, ErrConflict
	}
	f, err = services.NormalizeFinancialReportFilter(kind, f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}

func financialFilterKey(kind authorization.FinancialReportKind, key string) bool {
	switch kind {
	case authorization.FinancialAging:
		return key == "customer_id" || key == "bucket" || key == "sort"
	case authorization.FinancialCustomers:
		return key == "customer_id" || key == "sort"
	case authorization.FinancialSettlement, authorization.FinancialCombined:
		return key == "customer_id" || key == "from" || key == "to" || key == "include_archived" || key == "sort"
	case authorization.FinancialCredit:
		return key == "customer_id" || key == "include_archived" || key == "sort"
	default:
		return false
	}
}

func financialAgingColumns() []string {
	return []string{"as_of", "timezone", "row_type", "customer_id", "customer", "invoice_id", "invoice_number", "due_date", "bucket", "invoice_count", "total_amount", "settled_amount", "outstanding_amount"}
}

func financialCustomerColumns() []string {
	return []string{"as_of", "timezone", "row_type", "customer_id", "customer", "invoice_count", "total_amount", "settled_amount", "outstanding_amount", "not_yet_due_amount", "overdue_1_30_amount", "overdue_31_60_amount", "overdue_61_90_amount", "overdue_91_plus_amount", "no_due_date_amount"}
}

func financialSettlementColumns() []string {
	return []string{"as_of", "timezone", "row_type", "section", "period_from", "period_to", "customer_id", "customer", "customer_archived", "event_type", "event_id", "original_event_type", "original_event_id", "effective_date", "entered_at", "entry_time_known", "invoice_id", "credit_id", "source_payment_id", "allocation_kind", "allocation_id", "source_received_date", "event_count", "event_amount", "cash_effect", "allocation_amount"}
}

func financialCreditColumns() []string {
	return []string{"as_of", "timezone", "row_type", "customer_id", "customer", "customer_archived", "credit_id", "source_payment_id", "source_received_date", "source_entered_at", "source_entry_time_known", "original_funded_amount", "current_available_amount"}
}

func financialCombinedColumns() []string {
	return append(financialSettlementColumns(), "source_entered_at", "source_entry_time_known", "original_funded_amount", "current_available_amount")
}
