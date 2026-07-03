package services

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestInvoiceServiceRecordAndDeletePaymentIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	invoice := client.Invoice.Create().
		SetInvoiceNumber(1001).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SaveX(ctx)

	svc := NewInvoiceService(client)
	if err := svc.RecordPayment(ctx, invoice.ID, Payment{Amount: 40, Method: "cash"}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	updated := client.Invoice.GetX(ctx, invoice.ID)
	payments := svc.Payments(updated)
	if len(payments) != 1 {
		t.Fatalf("len(payments) = %d, want 1", len(payments))
	}
	if payments[0].ID == "" || strings.HasPrefix(payments[0].ID, "legacy-") {
		t.Fatalf("recorded payment ID = %q, want new stable ID", payments[0].ID)
	}

	deleted, err := svc.DeletePayment(ctx, invoice.ID, payments[0].ID)
	if err != nil {
		t.Fatalf("DeletePayment: %v", err)
	}
	if deleted.Amount != 40 {
		t.Fatalf("deleted amount = %.2f, want 40.00", deleted.Amount)
	}
	remaining := svc.Payments(client.Invoice.GetX(ctx, invoice.ID))
	if len(remaining) != 0 {
		t.Fatalf("len(remaining) = %d, want 0", len(remaining))
	}
}

func TestInvoiceServiceDeleteLegacyPaymentIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	invoice := client.Invoice.Create().
		SetInvoiceNumber(1002).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SetPayments(`[{"amount":25,"method":"check","reference":"123","date":"2026-07-03","notes":""}]`).
		SaveX(ctx)

	svc := NewInvoiceService(client)
	payments := svc.Payments(invoice)
	if len(payments) != 1 || payments[0].ID == "" {
		t.Fatalf("legacy payment was not readable with an ID: %#v", payments)
	}
	if _, err := svc.DeletePayment(ctx, invoice.ID, payments[0].ID); err != nil {
		t.Fatalf("DeletePayment legacy: %v", err)
	}
	remaining := svc.Payments(client.Invoice.GetX(ctx, invoice.ID))
	if len(remaining) != 0 {
		t.Fatalf("len(remaining) = %d, want 0", len(remaining))
	}
}

func TestInvoiceServiceDeletePaymentRejectsMissingIDIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	invoice := client.Invoice.Create().
		SetInvoiceNumber(1003).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SetPayments(SerializePayments([]Payment{{ID: "payment-1", Amount: 10}})).
		SaveX(ctx)

	_, err := NewInvoiceService(client).DeletePayment(ctx, invoice.ID, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("DeletePayment missing error = %v, want not found", err)
	}
	payments := NewInvoiceService(client).Payments(client.Invoice.GetX(ctx, invoice.ID))
	if len(payments) != 1 || payments[0].ID != "payment-1" {
		t.Fatalf("payments after failed delete = %#v, want original payment", payments)
	}
}
