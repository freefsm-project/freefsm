package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type Payment struct {
	ID        string  `json:"id,omitempty"`
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
	backfillPaymentIDs(payments)
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

func ensurePaymentID(payment *Payment) {
	if strings.TrimSpace(payment.ID) == "" {
		payment.ID = uuid.NewString()
	}
}

func backfillPaymentIDs(payments []Payment) bool {
	changed := false
	for i := range payments {
		if strings.TrimSpace(payments[i].ID) == "" {
			payments[i].ID = legacyPaymentID(i, payments[i])
			changed = true
		}
	}
	return changed
}

func legacyPaymentID(index int, payment Payment) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%.17g|%s|%s|%s|%s", index, payment.Amount, payment.Method, payment.Reference, payment.Date, payment.Notes)))
	return "legacy-" + hex.EncodeToString(h[:8])
}
