package services

import (
	"context"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestTagLinkServiceAttachListDetachIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Priority").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Tagged Customer").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	link, err := svc.Attach(ctx, tag.ID, ref)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if link.ObjectType != ref.ObjectType() || link.ObjectID != ref.ObjectID() {
		t.Fatalf("link ref = %s %d, want %s %d", link.ObjectType, link.ObjectID, ref.ObjectType(), ref.ObjectID())
	}

	tags, err := svc.ListForObject(ctx, ref)
	if err != nil {
		t.Fatalf("ListForObject: %v", err)
	}
	if len(tags) != 1 || tags[0].ID != tag.ID {
		t.Fatalf("tags = %#v", tags)
	}

	links, err := svc.ListObjectsWithTag(ctx, tag.ID, objectref.TypeCustomer)
	if err != nil {
		t.Fatalf("ListObjectsWithTag: %v", err)
	}
	if len(links) != 1 || links[0].ObjectID != ref.ID {
		t.Fatalf("links = %#v", links)
	}

	if err := svc.Detach(ctx, tag.ID, ref); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	tags, err = svc.ListForObject(ctx, ref)
	if err != nil {
		t.Fatalf("ListForObject after detach: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("tags after detach = %#v", tags)
	}
}

func TestTagLinkServiceListsTagsAfterTargetArchivedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Archived Listable").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Archived Listable Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	if _, err := svc.Attach(ctx, tag.ID, ref); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	tags, err := svc.ListForObject(ctx, ref)
	if err != nil {
		t.Fatalf("ListForObject archived target: %v", err)
	}
	if len(tags) != 1 || tags[0].ID != tag.ID {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestTagLinkServiceDuplicateAttachIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Duplicate").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Duplicate Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	if _, err := svc.Attach(ctx, tag.ID, ref); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := svc.Attach(ctx, tag.ID, ref); err == nil {
		t.Fatal("duplicate Attach error = nil")
	}
}

func TestTagLinkServiceRejectsInvalidAndUnsupportedRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Invalid").SaveX(ctx)

	if _, err := svc.Attach(ctx, tag.ID, objectref.Ref{Type: objectref.TypeCustomer}); err == nil {
		t.Fatal("Attach invalid ref error = nil")
	}

	item := client.Item.Create().SetName("Unsupported Item").SaveX(ctx)
	if _, err := svc.Attach(ctx, tag.ID, objectref.New(objectref.TypeItem, item.ID)); err == nil {
		t.Fatal("Attach unsupported ref error = nil")
	}
	if _, err := svc.ListObjectsWithTag(ctx, tag.ID, objectref.TypeItem); err == nil {
		t.Fatal("ListObjectsWithTag unsupported type error = nil")
	}
}

func TestTagLinkServiceRejectsMissingAndArchivedTargetsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Target Validation").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetDisplayName("Archived Target").SetDeletedAt(time.Now()).SaveX(ctx)

	if _, err := svc.Attach(ctx, tag.ID, objectref.New(objectref.TypeCustomer, 999999)); err == nil {
		t.Fatal("Attach missing target error = nil")
	}
	if _, err := svc.Attach(ctx, tag.ID, objectref.New(objectref.TypeCustomer, archivedCustomer.ID)); err == nil {
		t.Fatal("Attach archived target error = nil")
	}
}

func TestTagLinkServiceRejectsDetachFromArchivedTargetIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	tag := client.Tag.Create().SetName("Archived Detach").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Archived Detach Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	if _, err := svc.Attach(ctx, tag.ID, ref); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	if err := svc.Detach(ctx, tag.ID, ref); err == nil {
		t.Fatal("Detach archived target error = nil")
	}
}

func TestTagLinkServiceRejectsMissingTagIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTagLinkService(client, objectref.NewEntDirectory(client))
	customer := client.Customer.Create().SetDisplayName("Missing Tag Target").SaveX(ctx)

	if _, err := svc.Attach(ctx, 999999, objectref.New(objectref.TypeCustomer, customer.ID)); err == nil {
		t.Fatal("Attach missing tag error = nil")
	}
}
