package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/go-chi/chi/v5"
)

type fakeSettlement struct {
	payment    settlement.RecordPaymentRequest
	actor      settlement.Actor
	invoiceErr error
}

func (f *fakeSettlement) RecordPayment(_ context.Context, a settlement.Actor, r settlement.RecordPaymentRequest) (settlement.Result, error) {
	f.actor, f.payment = a, r
	return settlement.Result{AppliedCents: r.AmountCents}, nil
}
func (*fakeSettlement) ApplyCredit(context.Context, settlement.Actor, settlement.ApplyCreditRequest) (settlement.Result, error) {
	return settlement.Result{}, nil
}
func (*fakeSettlement) RefundCredit(context.Context, settlement.Actor, settlement.RefundCreditRequest) (settlement.Result, error) {
	return settlement.Result{}, nil
}
func (*fakeSettlement) ReversePayment(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error) {
	return settlement.Result{}, nil
}
func (*fakeSettlement) ReverseCreditApplication(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error) {
	return settlement.Result{}, nil
}
func (*fakeSettlement) ReverseRefund(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error) {
	return settlement.Result{}, nil
}
func (f *fakeSettlement) InvoiceSettlement(context.Context, settlement.Actor, int64) (settlement.InvoiceSettlement, error) {
	return settlement.InvoiceSettlement{}, f.invoiceErr
}

func TestCustomerFinancialSummaryPropagatesSettlementQueryFailure(t *testing.T) {
	want := errors.New("query failed")
	_, err := customerFinancialSummary(context.Background(), &fakeSettlement{invoiceErr: want}, settlement.Actor{}, []*ent.Invoice{{ID: 1}}, nil, time.UTC)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
func (*fakeSettlement) CustomerSettlement(context.Context, settlement.Actor, int64) (settlement.CustomerSettlement, error) {
	return settlement.CustomerSettlement{}, nil
}

func TestParseCents(t *testing.T) {
	tests := map[string]int64{"0.01": 1, "1": 100, "1.2": 120, "001.20": 120, " 12.34 ": 1234}
	for input, want := range tests {
		got, err := parseCents(input)
		if err != nil || got != want {
			t.Errorf("parseCents(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1", ".5", "1.", "1.234", "1e2", "NaN", "+1", "1 2", "92233720368547759"} {
		if _, err := parseCents(input); err == nil {
			t.Errorf("parseCents(%q) unexpectedly succeeded", input)
		}
	}
}

func TestRecordPaymentBuildsSettlementCommandAndRedirects(t *testing.T) {
	fake := &fakeSettlement{}
	h := &InvoiceHandler{settlement: fake}
	form := url.Values{"amount": {"12.34"}, "method": {"check"}, "date": {"2026-07-11"}, "reference": {"A1"}, "idempotency_key": {"stable-key"}}
	r := httptest.NewRequest(http.MethodPost, "/invoices/42/payments", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, middleware.UserKey, &middleware.UserInfo{ID: 3, CompanyID: 9, Role: "dispatcher"})
	w := httptest.NewRecorder()
	h.RecordPayment(w, r.WithContext(ctx))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if fake.actor.ID != 3 || fake.actor.CompanyID != 9 || fake.payment.InvoiceID != 42 || fake.payment.AmountCents != 1234 || fake.payment.Operation.Key != "stable-key" {
		t.Fatalf("command=%#v actor=%#v", fake.payment, fake.actor)
	}
}
