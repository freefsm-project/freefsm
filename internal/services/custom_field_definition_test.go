package services

import (
	"context"
	"errors"
	"testing"
)

func TestCustomFieldConversionKeyValidation(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewCustomFieldDefinitionService(client)
	if _, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "customer", Name: "Bad", FieldType: "text", Options: "[]", ConversionKey: "shared"}); err == nil {
		t.Fatal("customer conversion key accepted")
	}
	d, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "estimate", Name: "Shared", FieldType: "text", Options: "[]", ConversionKey: " shared "})
	if err != nil {
		t.Fatal(err)
	}
	if d.ConversionKey == nil || *d.ConversionKey != "shared" {
		t.Fatalf("conversion key=%v", d.ConversionKey)
	}
	empty := ""
	d, err = svc.Update(ctx, d.ID, CustomFieldDefUpdateParams{ConversionKey: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if d.ConversionKey != nil {
		t.Fatalf("conversion key not cleared: %v", d.ConversionKey)
	}
}

func TestCustomFieldConversionPairCompatibility(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewCustomFieldDefinitionService(client)
	if _, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "estimate", Name: "Estimate", FieldType: "select", Options: `["A","B"]`, ConversionKey: "shared"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "invoice", Name: "Invoice", FieldType: "text", Options: "[]", ConversionKey: "shared"}); !errors.Is(err, ErrIncompatibleConversionField) {
		t.Fatalf("type mismatch error=%v", err)
	}
	if _, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "invoice", Name: "Invoice", FieldType: "select", Options: `["A","C"]`, ConversionKey: "shared"}); !errors.Is(err, ErrIncompatibleConversionField) {
		t.Fatalf("option mismatch error=%v", err)
	}
	paired, err := svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "invoice", Name: "Renamed label", FieldType: "select", Options: `["A","B"]`, ConversionKey: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	changed := "text"
	if _, err = svc.Update(ctx, paired.ID, CustomFieldDefUpdateParams{FieldType: &changed}); !errors.Is(err, ErrIncompatibleConversionField) {
		t.Fatalf("update mismatch error=%v", err)
	}
	if _, err = svc.Create(ctx, CustomFieldDefCreateParams{ObjectType: "invoice", Name: "Duplicate", FieldType: "select", Options: `["A","B"]`, ConversionKey: "shared"}); !errors.Is(err, ErrDuplicateConversionKey) {
		t.Fatalf("duplicate error=%v", err)
	}
}
