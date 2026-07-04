package services

import (
	"context"
	"testing"
)

func TestInvitationServiceCreatesSingleUseInvites(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := client.User.Create().
		SetEmail("invite@example.test").
		SetPasswordHash("hash").
		SetName("Invited User").
		SetRole("tech").
		SetIsActive(false).
		SaveX(ctx)

	svc := NewInvitationService(client)
	token, err := svc.CreateInvite(ctx, user.ID)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	uid, err := svc.ValidateInvite(ctx, token)
	if err != nil {
		t.Fatalf("validate invite: %v", err)
	}
	if uid != user.ID {
		t.Fatalf("validated user id = %d, want %d", uid, user.ID)
	}
	if err := svc.ConsumeInvite(ctx, token); err != nil {
		t.Fatalf("consume invite: %v", err)
	}
	if _, err := svc.ValidateInvite(ctx, token); err == nil {
		t.Fatalf("consumed invite validated successfully")
	}
}

func TestInvitationServiceInvalidatesPreviousInvites(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := client.User.Create().
		SetEmail("resend@example.test").
		SetPasswordHash("hash").
		SetName("Resend User").
		SetRole("tech").
		SetIsActive(false).
		SaveX(ctx)

	svc := NewInvitationService(client)
	oldToken, err := svc.CreateInvite(ctx, user.ID)
	if err != nil {
		t.Fatalf("create old invite: %v", err)
	}
	newToken, err := svc.CreateInvite(ctx, user.ID)
	if err != nil {
		t.Fatalf("create new invite: %v", err)
	}
	if _, err := svc.ValidateInvite(ctx, oldToken); err == nil {
		t.Fatalf("old invite validated after resend")
	}
	if _, err := svc.ValidateInvite(ctx, newToken); err != nil {
		t.Fatalf("new invite did not validate: %v", err)
	}
}
