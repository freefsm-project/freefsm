package services

import "encoding/json"

type Payment struct {
	Amount    float64 `json:"amount"`
	Method    string  `json:"method"`
	Reference string  `json:"reference"`
	Date      string  `json:"date"`
	Notes     string  `json:"notes"`
}

func ParsePayments(s string) ([]Payment, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var payments []Payment
	if err := json.Unmarshal([]byte(s), &payments); err != nil {
		return nil, err
	}
	return payments, nil
}

func SerializePayments(payments []Payment) string {
	if len(payments) == 0 {
		return "[]"
	}
	b, err := json.Marshal(payments)
	if err != nil {
		return "[]"
	}
	return string(b)
}

var PaymentMethods = []string{"cash", "check", "credit_card", "transfer", "other"}
