package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLineItemPersistenceRejectsInvalidDataWithoutWritesIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	invalid := []LineItem{{Title: "Invalid", Quantity: 0}}

	jobCount := client.Job.Query().CountX(ctx)
	if _, err := NewJobService(client).Create(ctx, 1, JobCreateParams{CustomerID: data.customerA.ID, JobType: "Invalid", LineItems: invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("job create error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Job.Query().CountX(ctx); got != jobCount {
		t.Fatalf("job count = %d, want %d", got, jobCount)
	}

	estimateCount := client.Estimate.Query().CountX(ctx)
	if _, err := NewEstimateService(client).Create(ctx, EstimateCreateParams{CustomerID: data.customerA.ID, Title: "Invalid", LineItems: invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("estimate create error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Estimate.Query().CountX(ctx); got != estimateCount {
		t.Fatalf("estimate count = %d, want %d", got, estimateCount)
	}

	invoiceCount := client.Invoice.Query().CountX(ctx)
	if _, err := NewInvoiceService(client).Create(ctx, InvoiceCreateParams{CustomerID: data.customerA.ID, Title: "Invalid", InvoiceDate: time.Now(), DueDate: time.Now(), LineItems: invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("invoice create error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Invoice.Query().CountX(ctx); got != invoiceCount {
		t.Fatalf("invoice count = %d, want %d", got, invoiceCount)
	}

	original := data.jobA.LineItems
	if _, err := NewJobService(client).Update(ctx, 1, data.jobA.ID, JobUpdateParams{LineItems: &invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("job update error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Job.GetX(ctx, data.jobA.ID).LineItems; got != original {
		t.Fatalf("job line items changed to %q, want %q", got, original)
	}

	original = data.estimateA.LineItems
	if _, err := NewEstimateService(client).Update(ctx, data.estimateA.ID, EstimateUpdateParams{LineItems: &invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("estimate update error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Estimate.GetX(ctx, data.estimateA.ID).LineItems; got != original {
		t.Fatalf("estimate line items changed to %q, want %q", got, original)
	}

	invoice := client.Invoice.Create().
		SetInvoiceNumber(9001).
		SetCustomerID(data.customerA.ID).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SaveX(ctx)
	original = invoice.LineItems
	if _, err := NewInvoiceService(client).Update(ctx, invoice.ID, InvoiceUpdateParams{LineItems: &invalid}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("invoice update error = %v, want ErrInvalidLineItem", err)
	}
	if got := client.Invoice.GetX(ctx, invoice.ID).LineItems; got != original {
		t.Fatalf("invoice line items changed to %q, want %q", got, original)
	}
}

func TestLineItemPersistencePreservesEmptyAndFractionalQuantitiesIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	service := NewJobService(client)

	items := []LineItem{{Title: "Fractional labor", UnitPrice: 20, Quantity: 1.25}}
	updated, err := service.Update(ctx, 1, data.jobA.ID, JobUpdateParams{LineItems: &items})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLineItems(updated.LineItems)
	if err != nil || len(decoded) != 1 || decoded[0].Quantity != 1.25 {
		t.Fatalf("fractional round trip = (%#v, %v)", decoded, err)
	}

	empty := []LineItem{}
	updated, err = service.Update(ctx, 1, data.jobA.ID, JobUpdateParams{LineItems: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LineItems != "[]" {
		t.Fatalf("explicit empty line items = %q, want []", updated.LineItems)
	}

	updated, err = service.Update(ctx, 1, data.jobA.ID, JobUpdateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LineItems != "[]" {
		t.Fatalf("absent update changed line items to %q", updated.LineItems)
	}
}

func TestConversionsRejectMalformedLineItemsBeforeWritesIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	statusService := NewStatusService(client)

	client.Job.UpdateOneID(data.jobA.ID).SetLineItems("[").SaveX(ctx)
	estimateCount := client.Estimate.Query().CountX(ctx)
	if _, err := NewEstimateService(client).CreateFromJob(ctx, data.jobA.ID, statusService, "0"); !errors.Is(err, ErrMalformedLineItems) {
		t.Fatalf("job conversion error = %v, want ErrMalformedLineItems", err)
	}
	if got := client.Estimate.Query().CountX(ctx); got != estimateCount {
		t.Fatalf("estimate count = %d, want %d", got, estimateCount)
	}

}

func TestDocumentPersistenceValidatesEffectiveTaxAndLineItemsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	estimates := NewEstimateService(client)
	invoices := NewInvoiceService(client)

	estimateCount := client.Estimate.Query().CountX(ctx)
	if _, err := estimates.Create(ctx, EstimateCreateParams{CustomerID: data.customerA.ID, Title: "Invalid tax", TaxRate: "invalid"}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("estimate create error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Estimate.Query().CountX(ctx); got != estimateCount {
		t.Fatalf("estimate count = %d, want %d", got, estimateCount)
	}

	invoiceCount := client.Invoice.Query().CountX(ctx)
	if _, err := invoices.Create(ctx, InvoiceCreateParams{CustomerID: data.customerA.ID, Title: "Invalid tax", InvoiceDate: time.Now(), DueDate: time.Now(), TaxRate: "invalid"}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("invoice create error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Invoice.Query().CountX(ctx); got != invoiceCount {
		t.Fatalf("invoice count = %d, want %d", got, invoiceCount)
	}

	invalidTax := "invalid"
	originalEstimate := client.Estimate.GetX(ctx, data.estimateA.ID)
	if _, err := estimates.Update(ctx, data.estimateA.ID, EstimateUpdateParams{TaxRate: &invalidTax}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("estimate tax update error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Estimate.GetX(ctx, data.estimateA.ID).TaxRate; got != originalEstimate.TaxRate {
		t.Fatalf("estimate tax rate changed to %q, want %q", got, originalEstimate.TaxRate)
	}

	invoice := client.Invoice.Create().
		SetInvoiceNumber(9002).
		SetCustomerID(data.customerA.ID).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SaveX(ctx)
	if _, err := invoices.Update(ctx, invoice.ID, InvoiceUpdateParams{TaxRate: &invalidTax}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("invoice tax update error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Invoice.GetX(ctx, invoice.ID).TaxRate; got != invoice.TaxRate {
		t.Fatalf("invoice tax rate changed to %q, want %q", got, invoice.TaxRate)
	}

	items := []LineItem{{Title: "Valid", Quantity: 1, UnitPrice: 10}}
	client.Estimate.UpdateOneID(data.estimateA.ID).SetTaxRate("invalid").SaveX(ctx)
	if _, err := estimates.Update(ctx, data.estimateA.ID, EstimateUpdateParams{LineItems: &items}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("estimate line-only update error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Estimate.GetX(ctx, data.estimateA.ID).LineItems; got != originalEstimate.LineItems {
		t.Fatalf("estimate line items changed to %q, want %q", got, originalEstimate.LineItems)
	}

	client.Invoice.UpdateOneID(invoice.ID).SetTaxRate("invalid").SaveX(ctx)
	if _, err := invoices.Update(ctx, invoice.ID, InvoiceUpdateParams{LineItems: &items}); !errors.Is(err, ErrInvalidTaxRate) {
		t.Fatalf("invoice line-only update error = %v, want ErrInvalidTaxRate", err)
	}
	if got := client.Invoice.GetX(ctx, invoice.ID).LineItems; got != invoice.LineItems {
		t.Fatalf("invoice line items changed to %q, want %q", got, invoice.LineItems)
	}
}

func TestDocumentUpdatesOnlyDecodePersistedLinesWhenRequiredIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	estimates := NewEstimateService(client)
	invoices := NewInvoiceService(client)
	taxRate := "5"
	title := "Unrelated update"

	client.Estimate.UpdateOneID(data.estimateA.ID).SetLineItems("[").SaveX(ctx)
	if _, err := estimates.Update(ctx, data.estimateA.ID, EstimateUpdateParams{TaxRate: &taxRate}); !errors.Is(err, ErrMalformedLineItems) {
		t.Fatalf("estimate tax-only update error = %v, want ErrMalformedLineItems", err)
	}
	if got := client.Estimate.GetX(ctx, data.estimateA.ID).TaxRate; got == taxRate {
		t.Fatalf("estimate tax rate changed despite malformed line items")
	}
	if _, err := estimates.Update(ctx, data.estimateA.ID, EstimateUpdateParams{Title: &title}); err != nil {
		t.Fatalf("unrelated estimate update was blocked: %v", err)
	}

	invoice := client.Invoice.Create().
		SetInvoiceNumber(9003).
		SetCustomerID(data.customerA.ID).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SetLineItems("[").
		SaveX(ctx)
	if _, err := invoices.Update(ctx, invoice.ID, InvoiceUpdateParams{TaxRate: &taxRate}); !errors.Is(err, ErrMalformedLineItems) {
		t.Fatalf("invoice tax-only update error = %v, want ErrMalformedLineItems", err)
	}
	if got := client.Invoice.GetX(ctx, invoice.ID).TaxRate; got == taxRate {
		t.Fatalf("invoice tax rate changed despite malformed line items")
	}
	if _, err := invoices.Update(ctx, invoice.ID, InvoiceUpdateParams{Title: &title}); err != nil {
		t.Fatalf("unrelated invoice update was blocked: %v", err)
	}
}
