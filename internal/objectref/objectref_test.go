package objectref

import (
	"context"
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	ref, err := Parse("customer", 42)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ref != (Ref{Type: TypeCustomer, ID: 42}) {
		t.Fatalf("Parse ref = %#v", ref)
	}

	if _, err := Parse("bogus", 1); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Parse unknown error = %v", err)
	}
	if _, err := Parse("customer", 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Parse invalid id error = %v", err)
	}
}

func TestFakeDirectoryTargetCompanyIDIsStrictAndRecordsCalls(t *testing.T) {
	ref := New(TypeInvoice, 42)
	d := &FakeDirectory{TargetCompanyIDs: map[Ref]*int64{ref: int64TestPointer(7)}}
	companyID, err := d.TargetCompanyID(context.Background(), ref)
	if err != nil || companyID != 7 {
		t.Fatalf("company=%d err=%v", companyID, err)
	}
	if len(d.TargetCompanyCalls) != 1 || d.TargetCompanyCalls[0] != ref {
		t.Fatalf("calls=%v", d.TargetCompanyCalls)
	}
	if _, err := d.TargetCompanyID(context.Background(), New(TypeInvoice, 43)); err == nil {
		t.Fatal("missing ownership error=nil")
	}
}

func int64TestPointer(v int64) *int64 { return &v }

func TestCapabilitiesAndAdminOnly(t *testing.T) {
	for _, typ := range AllTypes() {
		if !typ.Has(CapActivity) {
			t.Fatalf("%s should support activity", typ)
		}
	}

	files := []Type{TypeCustomer, TypeJob, TypeProject, TypeEstimate, TypeInvoice, TypeAsset}
	for _, typ := range files {
		if !typ.Has(CapFiles) {
			t.Fatalf("%s should support files", typ)
		}
	}

	archive := []Type{TypeCustomer, TypeJob, TypeProject, TypeEstimate, TypeInvoice, TypeAsset, TypeItem}
	for _, typ := range archive {
		if !typ.Has(CapArchive) {
			t.Fatalf("%s should support archive", typ)
		}
	}

	tags := []Type{TypeCustomer, TypeJob, TypeProject, TypeEstimate, TypeInvoice, TypeAsset}
	for _, typ := range tags {
		if !typ.Has(CapTags) {
			t.Fatalf("%s should support tags", typ)
		}
	}

	comments := []Type{TypeCustomer, TypeJob, TypeProject, TypeEstimate, TypeInvoice, TypeAsset}
	for _, typ := range comments {
		if !typ.Has(CapComments) {
			t.Fatalf("%s should support comments", typ)
		}
	}
	if TypeItem.Has(CapTags) {
		t.Fatal("item should not support tags")
	}
	if TypeItem.Has(CapFiles) {
		t.Fatal("item should not support files")
	}
	if TypeItem.Has(CapComments) {
		t.Fatal("item should not support comments")
	}

	admin := []Type{TypeAssetType, TypeAssetStatus, TypeCompanySettings, TypeCustomField, TypeJobStatus, TypeTag, TypeUser}
	for _, typ := range admin {
		if !typ.AdminOnly() {
			t.Fatalf("%s should be admin-only", typ)
		}
	}
	if TypeCustomer.AdminOnly() {
		t.Fatal("customer should not be admin-only")
	}
}

func TestURLIsPure(t *testing.T) {
	ref := Ref{Type: TypeCustomer, ID: 7}
	got, ok := URL(ref)
	if !ok || got != "/customers/7" {
		t.Fatalf("URL = %q, %v", got, ok)
	}
	if ref != (Ref{Type: TypeCustomer, ID: 7}) {
		t.Fatalf("URL mutated ref to %#v", ref)
	}
	if got, ok := URL(Ref{Type: TypeAssetType, ID: 2}); !ok || got != "/settings/assets" {
		t.Fatalf("asset type URL = %q, %v", got, ok)
	}
	if got, ok := URL(Ref{Type: TypeTag, ID: 2}); !ok || got != "/tags/2" {
		t.Fatalf("tag URL = %q, %v", got, ok)
	}
}

func TestFakeDirectory(t *testing.T) {
	ref := Ref{Type: TypeCustomer, ID: 1}
	dir := &FakeDirectory{
		Names:  map[Ref]string{ref: "Ada Lovelace"},
		Active: map[Ref]bool{ref: true},
		Any:    map[Ref]bool{ref: true},
		URLs:   map[Ref]string{ref: "/custom/customer"},
	}

	name, err := dir.DisplayName(context.Background(), ref)
	if err != nil {
		t.Fatalf("DisplayName returned error: %v", err)
	}
	if name != "Ada Lovelace" {
		t.Fatalf("DisplayName = %q", name)
	}
	if ok, err := dir.Exists(context.Background(), ref, ExistsActive); err != nil || !ok {
		t.Fatalf("Exists active = %v, %v", ok, err)
	}
	if got, ok := dir.URL(ref); !ok || got != "/custom/customer" {
		t.Fatalf("URL = %q, %v", got, ok)
	}
	if desc, ok := dir.Describe(TypeCustomer); !ok || desc.SingularName != "customer" {
		t.Fatalf("Describe = %#v, %v", desc, ok)
	}
	if !dir.Supports(TypeCustomer, CapFiles) {
		t.Fatal("Supports customer files = false")
	}
	if dir.Supports(TypeItem, CapFiles) {
		t.Fatal("Supports item files = true")
	}
}

func TestExistsActiveRequiresArchiveCapability(t *testing.T) {
	dir := &FakeDirectory{}
	_, err := dir.Exists(context.Background(), Ref{Type: TypeTimeEntry, ID: 1}, ExistsActive)
	if !errors.Is(err, ErrActiveUnsupported) {
		t.Fatalf("Exists active error = %v", err)
	}
}

func TestFallbackName(t *testing.T) {
	cases := map[Ref]string{
		{Type: TypeCustomer, ID: 3}:        "customer #3",
		{Type: TypeTimeEntry, ID: 3}:       "time entry #3",
		{Type: TypeAssetType, ID: 3}:       "asset type #3",
		{Type: TypeAssetStatus, ID: 3}:     "asset status #3",
		{Type: TypeJobStatus, ID: 3}:       "job status #3",
		{Type: TypeCompanySettings, ID: 3}: "Company Settings",
		{Type: TypeCustomField, ID: 3}:     "custom field #3",
	}
	for ref, want := range cases {
		if got := fallbackName(ref); got != want {
			t.Fatalf("fallbackName(%#v) = %q, want %q", ref, got, want)
		}
	}
}
