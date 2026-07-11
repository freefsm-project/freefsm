package services

import (
	"context"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestCommentServiceUsesObjectRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	user := client.User.Create().SetEmail("commenter@example.test").SetPasswordHash("hash").SetName("Commenter").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Comment Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	created, err := svc.Create(ctx, ref, user.ID, "Needs follow-up")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ObjectType != ref.ObjectType() || created.ObjectID != ref.ObjectID() {
		t.Fatalf("created ref = %s %d, want %s %d", created.ObjectType, created.ObjectID, ref.ObjectType(), ref.ObjectID())
	}

	comments, err := svc.ListForObject(ctx, ref)
	if err != nil {
		t.Fatalf("ListForObject: %v", err)
	}
	if len(comments) != 1 || comments[0].Content != "Needs follow-up" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestCommentServiceRejectsInvalidRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	invalid := objectref.Ref{Type: objectref.TypeCustomer}

	if _, err := svc.Create(ctx, invalid, 1, "bad"); err == nil {
		t.Fatal("Create invalid ref error = nil")
	}
	if _, err := svc.ListForObject(ctx, invalid); err == nil {
		t.Fatal("ListForObject invalid ref error = nil")
	}
}

func TestCommentServiceListsArchivedTargetsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	user := client.User.Create().SetEmail("archived-commenter@example.test").SetPasswordHash("hash").SetName("Archived Commenter").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Archived Comment Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	if _, err := svc.Create(ctx, ref, user.ID, "before archive"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	comments, err := svc.ListForObject(ctx, ref)
	if err != nil {
		t.Fatalf("ListForObject archived target: %v", err)
	}
	if len(comments) != 1 || comments[0].Content != "before archive" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestCommentServiceRejectsArchivedCreateIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	user := client.User.Create().SetEmail("archived-create@example.test").SetPasswordHash("hash").SetName("Archived Create").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Archived Create Target").SetDeletedAt(time.Now()).SaveX(ctx)

	if _, err := svc.Create(ctx, objectref.New(objectref.TypeCustomer, customer.ID), user.ID, "blocked"); err == nil {
		t.Fatal("Create archived target error = nil")
	}
}

func TestCommentServiceRejectsArchivedDeleteIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	user := client.User.Create().SetEmail("archived-delete@example.test").SetPasswordHash("hash").SetName("Archived Delete").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Archived Delete Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	comment, err := svc.Create(ctx, ref, user.ID, "cannot delete after archive")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	if err := svc.Delete(ctx, comment.ID); err == nil {
		t.Fatal("Delete archived target error = nil")
	}
}

func TestCommentServiceRejectsMissingAndUnsupportedTargetsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	user := client.User.Create().SetEmail("bad-comment-target@example.test").SetPasswordHash("hash").SetName("Bad Target").SetRole("dispatcher").SaveX(ctx)
	item := client.Item.Create().SetName("Unsupported Comment Target").SaveX(ctx)

	if _, err := svc.ListForObject(ctx, objectref.New(objectref.TypeCustomer, 999999)); err == nil {
		t.Fatal("ListForObject missing target error = nil")
	}
	if _, err := svc.Create(ctx, objectref.New(objectref.TypeCustomer, 999999), user.ID, "missing"); err == nil {
		t.Fatal("Create missing target error = nil")
	}
	if _, err := svc.ListForObject(ctx, objectref.New(objectref.TypeItem, item.ID)); err == nil {
		t.Fatal("ListForObject unsupported target error = nil")
	}
	if _, err := svc.Create(ctx, objectref.New(objectref.TypeItem, item.ID), user.ID, "unsupported"); err == nil {
		t.Fatal("Create unsupported target error = nil")
	}
}
