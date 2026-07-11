package services

import (
	"context"
	"testing"

	"github.com/MartialM1nd/freefsm/internal/objectref"
)

func TestCommentServiceUsesObjectRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client)
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
	svc := NewCommentService(client)
	invalid := objectref.Ref{Type: objectref.TypeCustomer}

	if _, err := svc.Create(ctx, invalid, 1, "bad"); err == nil {
		t.Fatal("Create invalid ref error = nil")
	}
	if _, err := svc.ListForObject(ctx, invalid); err == nil {
		t.Fatal("ListForObject invalid ref error = nil")
	}
}
