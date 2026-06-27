package services

import (
	"testing"

	"github.com/MartialM1nd/freefsm/internal/ent"
)

func TestFormatInvoiceNumberHonorsEmptyPrefix(t *testing.T) {
	got := FormatInvoiceNumber(7, &ent.CompanySettings{InvoicePrefix: ""})
	if got != "00007" {
		t.Fatalf("FormatInvoiceNumber empty prefix = %q, want %q", got, "00007")
	}
}

func TestFormatInvoiceNumberUsesDefaultForNilSettings(t *testing.T) {
	got := FormatInvoiceNumber(7, nil)
	if got != "INV-00007" {
		t.Fatalf("FormatInvoiceNumber nil settings = %q, want %q", got, "INV-00007")
	}
}

func TestFormatInvoiceNumberUsesCustomPrefix(t *testing.T) {
	got := FormatInvoiceNumber(7, &ent.CompanySettings{InvoicePrefix: "AR-"})
	if got != "AR-00007" {
		t.Fatalf("FormatInvoiceNumber custom prefix = %q, want %q", got, "AR-00007")
	}
}

func TestFormatEstimateNumberHonorsEmptyPrefix(t *testing.T) {
	got := FormatEstimateNumber(7, &ent.CompanySettings{EstimatePrefix: ""})
	if got != "00007" {
		t.Fatalf("FormatEstimateNumber empty prefix = %q, want %q", got, "00007")
	}
}

func TestFormatEstimateNumberUsesDefaultForNilSettings(t *testing.T) {
	got := FormatEstimateNumber(7, nil)
	if got != "EST-00007" {
		t.Fatalf("FormatEstimateNumber nil settings = %q, want %q", got, "EST-00007")
	}
}

func TestFormatEstimateNumberUsesCustomPrefix(t *testing.T) {
	got := FormatEstimateNumber(7, &ent.CompanySettings{EstimatePrefix: "Q-"})
	if got != "Q-00007" {
		t.Fatalf("FormatEstimateNumber custom prefix = %q, want %q", got, "Q-00007")
	}
}
