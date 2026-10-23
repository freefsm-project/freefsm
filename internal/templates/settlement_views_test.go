package templates

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	htmlnode "golang.org/x/net/html"
)

func TestInvoiceShowRendersSeparateSettlementAndReversedHistory(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	data := InvoiceDetail{ID: 7, Number: 7, CustomerID: 2, StatusName: "Sent", Settlement: settlement.InvoiceSettlement{State: settlement.PartiallyPaid, TotalCents: 10000, SettledCents: 2500, AmountDueCents: 7500, Payments: []settlement.PaymentEntry{{ID: uuid.New(), AmountCents: 2500, AppliedCents: 2500, ReceivedDate: now, Reversal: &settlement.ReversalEntry{Reason: "duplicate", EffectiveDate: now}}}}}
	var out bytes.Buffer
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "dispatcher"})
	ctx = WithDetailAccess(ctx, DetailAccess{"settlement": true, "pricing": true})
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

func TestCustomerShowCreditRequiresIndependentCreditAndHistoryFacets(t *testing.T) {
	data := CustomerShowPageData{Customer: CustomerDetail{ID: 2, DisplayName: "Acme"}, Settlement: settlement.CustomerSettlement{AvailableCreditCents: 500, Sources: []settlement.CreditSource{{ID: uuid.New(), SourcePaymentID: uuid.New(), InvoiceID: 7, OriginalCents: 1000, AvailableCents: 500}}, Refunds: []settlement.RefundEntry{{ID: uuid.New(), AmountCents: 500, Reason: "requested", Reversal: &settlement.ReversalEntry{Reason: "returned"}}}}}
	render := func(financial bool) string {
		data.ShowFinancial = false
		data.ShowCredit = financial
		data.ShowSettlement = financial
		var out bytes.Buffer
		ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "custom", CustomerView: true, CustomerFinancial: financial, AuthorizationLoaded: true})
		if err := CustomerShow(data).Render(ctx, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	admin := render(true)
	for _, want := range []string{"Customer Credit", "$5.00", "requested", "returned"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin view missing %q", want)
		}
	}
	if strings.Contains(render(false), "Customer Credit") {
		t.Error("technician view exposed customer credit")
	}
}

func TestSettlementReversalFormsUseViewDataIdempotencyKeys(t *testing.T) {
	date := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	invoicePayment, unappliedPayment, refund, application := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// InvoiceShow now links to the office workspace; it no longer owns these
	// forms. Exercise the public page templates that actually submit reversals.
	refundPage := settlement.RefundPage{
		CustomerID: 2, Today: "2026-07-11", ShowHistory: true, CanReverseRefund: true,
		Payments: []settlement.RefundPayment{
			{ID: invoicePayment, InvoiceID: 7, Date: date, CanReverse: true, Key: "stable-invoice-payment-reversal-key"},
			{ID: unappliedPayment, Date: date, CanReverse: true, Key: "stable-unapplied-payment-reversal-key"},
		},
		Refunds: []settlement.RefundEntry{{ID: refund, EffectiveDate: date, ReversalKey: "stable-refund-reversal-key"}},
	}
	applicationPage := settlement.ApplicationPage{InvoiceID: 7, Today: "2026-07-11", ShowHistory: true, CanReverse: true,
		Applications: []settlement.CreditApplicationEntry{{ID: application, EffectiveDate: date, ReversalKey: "stable-application-reversal-key"}},
	}
	for _, tc := range []struct {
		name   string
		render func(*bytes.Buffer) error
		want   map[string]string
	}{
		{"refund-workspace", func(out *bytes.Buffer) error {
			return CustomerRefunds(refundPage, "unused-create-key").Render(context.Background(), out)
		}, map[string]string{
			fmt.Sprintf("/invoices/7/payments/%s/reverse", invoicePayment):    "stable-invoice-payment-reversal-key",
			fmt.Sprintf("/customers/2/payments/%s/reverse", unappliedPayment): "stable-unapplied-payment-reversal-key",
			fmt.Sprintf("/customers/2/credit/refunds/%s/reverse", refund):     "stable-refund-reversal-key",
		}},
		{"application-workspace", func(out *bytes.Buffer) error {
			return CreditApplications(applicationPage, "unused-create-key").Render(context.Background(), out)
		}, map[string]string{
			fmt.Sprintf("/invoices/7/credit-applications/%s/reverse", application): "stable-application-reversal-key",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first, second bytes.Buffer
			for _, out := range []*bytes.Buffer{&first, &second} {
				if err := tc.render(out); err != nil {
					t.Fatal(err)
				}
				root, err := htmlnode.Parse(strings.NewReader(out.String()))
				if err != nil {
					t.Fatal(err)
				}
				found := map[string][]string{}
				var walk func(*htmlnode.Node, string)
				walk = func(n *htmlnode.Node, action string) {
					attr := func(key string) string {
						for _, a := range n.Attr {
							if a.Key == key {
								return a.Val
							}
						}
						return ""
					}
					if n.Type == htmlnode.ElementNode && n.Data == "form" {
						action = attr("action")
					}
					if n.Type == htmlnode.ElementNode && n.Data == "input" && attr("name") == "idempotency_key" {
						found[action] = append(found[action], attr("value"))
					}
					for child := n.FirstChild; child != nil; child = child.NextSibling {
						walk(child, action)
					}
				}
				walk(root, "")
				if len(found) != len(tc.want) {
					t.Fatalf("action/key fields=%v, want %v", found, tc.want)
				}
				for action, key := range tc.want {
					if keys := found[action]; len(keys) != 1 || keys[0] != key {
						t.Fatalf("action %s keys=%v, want exactly supplied key %q", action, keys, key)
					}
				}
			}
			if first.String() != second.String() {
				t.Fatal("re-rendering identical view data changed the reversal forms")
			}
		})
	}
}
