package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestInvoiceShowRendersSeparateSettlementAndReversedHistory(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	data := InvoiceDetail{ID: 7, Number: 7, CustomerID: 2, StatusName: "Sent", Settlement: settlement.InvoiceSettlement{State: settlement.PartiallyPaid, TotalCents: 10000, SettledCents: 2500, AmountDueCents: 7500, Payments: []settlement.PaymentEntry{{ID: uuid.New(), AmountCents: 2500, AppliedCents: 2500, ReceivedDate: now, Reversal: &settlement.ReversalEntry{Reason: "duplicate", EffectiveDate: now}}}}}
	var out bytes.Buffer
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "dispatcher"})
	if err := InvoiceShow(data).Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, want := range []string{"Sent", "partially_paid", "$100.00", "$25.00", "$75.00", "Reversed", "duplicate"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered invoice missing %q", want)
		}
	}
}

func TestCustomerShowCreditIsRoleVisibleAndIncludesRefundHistory(t *testing.T) {
	data := CustomerShowPageData{Customer: CustomerDetail{ID: 2, DisplayName: "Acme"}, Settlement: settlement.CustomerSettlement{AvailableCreditCents: 500, Sources: []settlement.CreditSource{{ID: uuid.New(), SourcePaymentID: uuid.New(), InvoiceID: 7, OriginalCents: 1000, AvailableCents: 500}}, Refunds: []settlement.RefundEntry{{ID: uuid.New(), AmountCents: 500, Reason: "requested", Reversal: &settlement.ReversalEntry{Reason: "returned"}}}}}
	render := func(role string) string {
		var out bytes.Buffer
		ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: role})
		if err := CustomerShow(data).Render(ctx, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	admin := render("admin")
	for _, want := range []string{"Customer Credit", "$5.00", "requested", "returned"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin view missing %q", want)
		}
	}
	if strings.Contains(render("technician"), "Customer Credit") {
		t.Error("technician view exposed customer credit")
	}
}

func TestSettlementReversalFormsUseViewDataIdempotencyKeys(t *testing.T) {
	key := "stable-payment-reversal-key"
	data := InvoiceDetail{ID: 7, CustomerID: 2, Settlement: settlement.InvoiceSettlement{Payments: []settlement.PaymentEntry{{ID: uuid.New(), ReversalKey: key, ReceivedDate: time.Now()}}}}
	var first, second bytes.Buffer
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "dispatcher"})
	if err := InvoiceShow(data).Render(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := InvoiceShow(data).Render(ctx, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() || !strings.Contains(first.String(), `value="`+key+`"`) {
		t.Fatal("re-rendered reversal form did not preserve its idempotency key")
	}
}
