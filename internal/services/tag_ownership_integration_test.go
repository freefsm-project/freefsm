package services

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestTagServicesIsolateCompaniesIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyA, companyB int64 = 101, 202
	tags := NewTagService(client)
	tagA, err := tags.Create(ctx, companyA, "A", "")
	if err != nil {
		t.Fatal(err)
	}
	tagB, err := tags.Create(ctx, companyB, "B", "")
	if err != nil {
		t.Fatal(err)
	}

	listed, err := tags.ListAll(ctx, companyA)
	if err != nil || len(listed) != 1 || listed[0].ID != tagA.ID {
		t.Fatalf("company A list=%v err=%v", listed, err)
	}
	if _, err = tags.GetByID(ctx, companyA, tagB.ID); err == nil {
		t.Fatal("cross-company GetByID succeeded")
	}
	if _, err = tags.Update(ctx, companyA, tagB.ID, "stolen", ""); err == nil {
		t.Fatal("cross-company Update succeeded")
	}
	if err = tags.Delete(ctx, companyA, tagB.ID); err == nil {
		t.Fatal("cross-company Delete succeeded")
	}

	refA := objectref.New(objectref.TypeCustomer, 11)
	refB := objectref.New(objectref.TypeCustomer, 22)
	directory := &objectref.FakeDirectory{
		Active:              map[objectref.Ref]bool{refA: true, refB: true},
		Any:                 map[objectref.Ref]bool{refA: true, refB: true},
		TagTargetCompanyIDs: map[objectref.Ref]*int64{refA: int64Pointer(companyA), refB: int64Pointer(companyB)},
	}
	links := NewTagLinkService(client, directory)
	if _, err = links.Attach(ctx, companyA, tagB.ID, refA); err == nil {
		t.Fatal("cross-company tag Attach succeeded")
	}
	if _, err = links.Attach(ctx, companyA, tagA.ID, refB); err == nil {
		t.Fatal("cross-company target Attach succeeded")
	}
	if _, err = links.Attach(ctx, companyA, tagA.ID, refA); err != nil {
		t.Fatalf("same-company Attach: %v", err)
	}
	if err = links.Detach(ctx, companyB, tagA.ID, refA); err == nil {
		t.Fatal("cross-company Detach succeeded")
	}
	if got, err := links.ListForObject(ctx, companyB, refB); err != nil || len(got) != 0 {
		t.Fatalf("company B object tags=%v err=%v", got, err)
	}
	if got, err := links.ListObjectsWithTag(ctx, companyA, tagA.ID, objectref.TypeCustomer); err != nil || len(got) != 1 {
		t.Fatalf("company A links=%v err=%v", got, err)
	}
}

func TestTagLinkServiceChecksOwnershipForEveryTaggableTypeIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyA, companyB int64 = 101, 202
	tag := client.Tag.Create().SetCompanyID(companyA).SetName("A").SaveX(ctx)
	types := []objectref.Type{objectref.TypeCustomer, objectref.TypeProject, objectref.TypeJob, objectref.TypeAsset, objectref.TypeEstimate, objectref.TypeInvoice}
	active := make(map[objectref.Ref]bool, len(types))
	owners := make(map[objectref.Ref]*int64, len(types))
	for i, typ := range types {
		ref := objectref.New(typ, int64(i+1))
		active[ref] = true
		owners[ref] = int64Pointer(companyB)
	}
	directory := &objectref.FakeDirectory{Active: active, Any: active, TagTargetCompanyIDs: owners}
	svc := NewTagLinkService(client, directory)
	for ref := range active {
		if _, err := svc.Attach(ctx, companyA, tag.ID, ref); err == nil {
			t.Errorf("Attach accepted %s owned by another company", ref.Type)
		}
	}
	if len(directory.TagTargetCompanyCalls) != len(types) {
		t.Fatalf("ownership calls=%d want %d", len(directory.TagTargetCompanyCalls), len(types))
	}
}

func int64Pointer(v int64) *int64 { return &v }
