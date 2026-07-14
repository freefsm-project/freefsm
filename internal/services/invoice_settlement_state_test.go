package services

import (
	"context"
	"testing"
	"time"
)

func TestInvoiceCreateAndUnsettledUpdateProjectCanonicalState(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	workflow := client.StatusWorkflow.Create().SetCompanyID(1).SetName("Invoices").SetObjectType("invoice").SaveX(ctx)
	client.Status.Create().SetCompanyID(1).SetWorkflowID(workflow.ID).SetName("Preparation").SetCategoryKey("invoice:draft").SetCategoryOrder(1).SetIsCategoryDefault(true).SaveX(ctx)
	client.CompanySettings.Create().SetBusinessName("Test").SaveX(ctx)
	svc := NewInvoiceService(client)

	zero, err := svc.Create(ctx, InvoiceCreateParams{CustomerID: data.customerA.ID, Title: "Zero", InvoiceDate: time.Now(), DueDate: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if zero.SettlementState != "paid" {
		t.Fatalf("zero-total state = %q, want paid", zero.SettlementState)
	}

	items := []LineItem{{Title: "Work", Quantity: 1, UnitPrice: 10}}
	updated, err := svc.Update(ctx, zero.ID, InvoiceUpdateParams{LineItems: &items})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SettlementState != "unpaid" {
		t.Fatalf("positive-total state = %q, want unpaid", updated.SettlementState)
	}
}

func TestInvoiceSettlementState(t *testing.T) {
	tests := []struct {
		total, settled int64
		want           string
	}{
		{0, 0, "paid"}, {100, 0, "unpaid"}, {100, 1, "partially_paid"}, {100, 100, "paid"},
	}
	for _, tt := range tests {
		if got := invoiceSettlementState(tt.total, tt.settled); got != tt.want {
			t.Fatalf("state(%d,%d) = %q, want %q", tt.total, tt.settled, got, tt.want)
		}
	}
}
