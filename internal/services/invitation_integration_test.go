package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/invitationtoken"
	"golang.org/x/crypto/bcrypt"
)

const invitationTestCompanyID int64 = 41

func TestInvitationServiceRenewsWithoutExistingToken(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u := createInvitedUser(t, ctx, client, invitationTestCompanyID, "recover")
	oldWelcomeSentAt := *u.WelcomeEmailSentAt

	eligible, err := svc.CanResendWelcome(ctx, invitationTestCompanyID, u.ID)
	if err != nil {
		t.Fatalf("check resend eligibility: %v", err)
	}
	if !eligible {
		t.Fatal("onboarding-pending user without a token was not eligible")
	}
	renewed, token, err := svc.RenewPendingInvite(ctx, invitationTestCompanyID, u.ID)
	if err != nil {
		t.Fatalf("renew pending invite: %v", err)
	}
	if renewed.IsActive {
		t.Fatal("renewal activated invited user")
	}
	if renewed.WelcomeEmailSentAt == nil || !renewed.WelcomeEmailSentAt.After(oldWelcomeSentAt) {
		t.Fatal("renewal did not update welcome_email_sent_at")
	}
	target, err := svc.ValidateInvite(ctx, token)
	if err != nil || target != (InvitationTarget{UserID: u.ID, CompanyID: invitationTestCompanyID}) {
		t.Fatalf("validate renewed invite = (%+v, %v)", target, err)
	}
}

func TestInvitationServiceRenewalScopesInvalidationByCompany(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u := createInvitedUser(t, ctx, client, invitationTestCompanyID, "scoped-renew")

	if _, err := svc.CreateInvite(ctx, invitationTestCompanyID, u.ID); err != nil {
		t.Fatalf("create company invitation: %v", err)
	}
	if _, err := createInvite(ctx, client, invitationTestCompanyID+1, u.ID, time.Now()); err != nil {
		t.Fatalf("create mismatched invitation: %v", err)
	}
	legacyToken := createUnownedInvite(t, ctx, client, u.ID)

	if _, _, err := svc.RenewPendingInvite(ctx, invitationTestCompanyID, u.ID); err != nil {
		t.Fatalf("renew pending invite: %v", err)
	}
	companyOpen := client.InvitationToken.Query().Where(
		invitationtoken.CompanyIDEQ(invitationTestCompanyID),
		invitationtoken.UserIDEQ(u.ID),
		invitationtoken.ConsumedAtIsNil(),
	).CountX(ctx)
	if companyOpen != 1 {
		t.Fatalf("company open invitation count = %d, want 1", companyOpen)
	}
	foreignOpen := client.InvitationToken.Query().Where(
		invitationtoken.CompanyIDEQ(invitationTestCompanyID+1),
		invitationtoken.UserIDEQ(u.ID),
		invitationtoken.ConsumedAtIsNil(),
	).CountX(ctx)
	if foreignOpen != 1 {
		t.Fatalf("foreign invitation was invalidated, open count = %d", foreignOpen)
	}
	if _, err := svc.ValidateInvite(ctx, legacyToken); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("unowned invitation validation error = %v, want ErrInvalidInvitation", err)
	}
}

func TestInvitationServiceRejectsIneligibleRenewalWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *ent.Client, *ent.User)
	}{
		{
			name: "active user",
			mutate: func(ctx context.Context, client *ent.Client, u *ent.User) {
				client.User.UpdateOneID(u.ID).SetIsActive(true).ExecX(ctx)
			},
		},
		{
			name: "completed established user",
			mutate: func(ctx context.Context, client *ent.Client, u *ent.User) {
				client.User.UpdateOneID(u.ID).SetOnboardingCompletedAt(time.Now()).ExecX(ctx)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := openPolicyTestClient(t)
			defer client.Close()
			ctx := context.Background()
			svc := NewInvitationService(client)
			u := createInvitedUser(t, ctx, client, invitationTestCompanyID, fmt.Sprintf("renew-rejected-%d", i))
			tt.mutate(ctx, client, u)
			before := client.User.GetX(ctx, u.ID)

			if eligible, err := svc.CanResendWelcome(ctx, invitationTestCompanyID, u.ID); err != nil || eligible {
				t.Fatalf("eligibility = (%v, %v), want (false, nil)", eligible, err)
			}
			if _, _, err := svc.RenewPendingInvite(ctx, invitationTestCompanyID, u.ID); !errors.Is(err, ErrWelcomeResendIneligible) {
				t.Fatalf("renew error = %v, want ErrWelcomeResendIneligible", err)
			}
			after := client.User.GetX(ctx, u.ID)
			if after.IsActive != before.IsActive || !sameTime(after.WelcomeEmailSentAt, before.WelcomeEmailSentAt) {
				t.Fatal("rejected renewal mutated user")
			}
			if got := client.InvitationToken.Query().Where(invitationtoken.UserIDEQ(u.ID)).CountX(ctx); got != 0 {
				t.Fatalf("rejected renewal created %d invitations", got)
			}
		})
	}
}

func TestInvitationServiceRejectsCrossCompanyRenewal(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u := createInvitedUser(t, ctx, client, invitationTestCompanyID, "foreign-renew")

	if eligible, err := svc.CanResendWelcome(ctx, invitationTestCompanyID+1, u.ID); err != nil || eligible {
		t.Fatalf("foreign eligibility = (%v, %v), want (false, nil)", eligible, err)
	}
	if _, _, err := svc.RenewPendingInvite(ctx, invitationTestCompanyID+1, u.ID); !errors.Is(err, ErrWelcomeResendIneligible) {
		t.Fatalf("foreign renewal error = %v, want ErrWelcomeResendIneligible", err)
	}
	if got := client.InvitationToken.Query().Where(invitationtoken.UserIDEQ(u.ID)).CountX(ctx); got != 0 {
		t.Fatalf("foreign renewal created %d invitations", got)
	}
}

func TestInvitationServiceAcceptsInviteExactlyOnce(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u, token := createPendingInvitation(t, ctx, client, svc, invitationTestCompanyID, "accept-once")

	target, err := svc.ValidateInvite(ctx, token)
	if err != nil || target != (InvitationTarget{UserID: u.ID, CompanyID: invitationTestCompanyID}) {
		t.Fatalf("validate invite = (%+v, %v)", target, err)
	}
	accepted, err := svc.AcceptInvite(ctx, token, "FirstPassword1!")
	if err != nil {
		t.Fatalf("accept invite: %v", err)
	}
	if !accepted.IsActive || accepted.OnboardingCompletedAt == nil {
		t.Fatal("accepted user was not activated and completed")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(accepted.PasswordHash), []byte("FirstPassword1!")); err != nil {
		t.Fatalf("accepted password mismatch: %v", err)
	}
	if _, err := svc.ValidateInvite(ctx, token); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("consumed validation error = %v, want ErrInvalidInvitation", err)
	}
	if _, err := svc.AcceptInvite(ctx, token, "OverwritePassword2!"); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("repeat acceptance error = %v, want ErrInvalidInvitation", err)
	}
	established := client.User.GetX(ctx, u.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(established.PasswordHash), []byte("FirstPassword1!")); err != nil {
		t.Fatal("repeat acceptance overwrote established password")
	}
}

func TestInvitationServiceRejectsMismatchedTokenCompany(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u := createInvitedUser(t, ctx, client, invitationTestCompanyID, "wrong-company")
	token, err := createInvite(ctx, client, invitationTestCompanyID+1, u.ID, time.Now())
	if err != nil {
		t.Fatalf("create mismatched invitation: %v", err)
	}
	originalHash := u.PasswordHash

	if _, err := svc.ValidateInvite(ctx, token); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("validation error = %v, want ErrInvalidInvitation", err)
	}
	if _, err := svc.AcceptInvite(ctx, token, "Password1!"); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("acceptance error = %v, want ErrInvalidInvitation", err)
	}
	unchanged := client.User.GetX(ctx, u.ID)
	if unchanged.IsActive || unchanged.OnboardingCompletedAt != nil || unchanged.PasswordHash != originalHash {
		t.Fatal("mismatched token company mutated user")
	}
}

func TestInvitationServiceRejectsAcceptanceForEstablishedUser(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *ent.Client, int64)
	}{
		{"active", func(ctx context.Context, client *ent.Client, id int64) {
			client.User.UpdateOneID(id).SetIsActive(true).ExecX(ctx)
		}},
		{"completed", func(ctx context.Context, client *ent.Client, id int64) {
			client.User.UpdateOneID(id).SetOnboardingCompletedAt(time.Now()).ExecX(ctx)
		}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := openPolicyTestClient(t)
			defer client.Close()
			ctx := context.Background()
			svc := NewInvitationService(client)
			u, token := createPendingInvitation(t, ctx, client, svc, invitationTestCompanyID, fmt.Sprintf("accept-rejected-%d", i))
			tt.mutate(ctx, client, u.ID)
			before := client.User.GetX(ctx, u.ID)

			if _, err := svc.AcceptInvite(ctx, token, "OverwritePassword1!"); !errors.Is(err, ErrInvalidInvitation) {
				t.Fatalf("acceptance error = %v, want ErrInvalidInvitation", err)
			}
			after := client.User.GetX(ctx, u.ID)
			if after.PasswordHash != before.PasswordHash {
				t.Fatal("rejected acceptance overwrote password")
			}
			open := client.InvitationToken.Query().Where(invitationtoken.UserIDEQ(u.ID), invitationtoken.ConsumedAtIsNil()).CountX(ctx)
			if open != 1 {
				t.Fatalf("rejected acceptance consumed token, open count = %d", open)
			}
		})
	}
}

func TestInvitationServiceConcurrentAcceptanceHasOneWinner(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u, token := createPendingInvitation(t, ctx, client, svc, invitationTestCompanyID, "concurrent-accept")
	passwords := []string{"ConcurrentPassword1!", "ConcurrentPassword2!"}
	type result struct {
		password string
		err      error
	}
	results := make(chan result, len(passwords))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, password := range passwords {
		wg.Add(1)
		go func(password string) {
			defer wg.Done()
			<-start
			_, err := svc.AcceptInvite(ctx, token, password)
			results <- result{password: password, err: err}
		}(password)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	winnerPassword := ""
	for result := range results {
		if result.err == nil {
			successes++
			winnerPassword = result.password
		} else if !errors.Is(result.err, ErrInvalidInvitation) {
			t.Fatalf("losing acceptance error = %v, want ErrInvalidInvitation", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent acceptances = %d, want 1", successes)
	}
	established := client.User.GetX(ctx, u.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(established.PasswordHash), []byte(winnerPassword)); err != nil {
		t.Fatal("stored password does not belong to acceptance winner")
	}
	if !established.IsActive || established.OnboardingCompletedAt == nil {
		t.Fatal("winning acceptance did not establish user")
	}
}

func TestInvitationServiceConcurrentRenewalLeavesOneOpenToken(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewInvitationService(client)
	u := createInvitedUser(t, ctx, client, invitationTestCompanyID, "concurrent-renew")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := svc.RenewPendingInvite(ctx, invitationTestCompanyID, u.ID)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("both concurrent renewals failed")
	}
	open := client.InvitationToken.Query().Where(
		invitationtoken.CompanyIDEQ(invitationTestCompanyID),
		invitationtoken.UserIDEQ(u.ID),
		invitationtoken.ConsumedAtIsNil(),
	).CountX(ctx)
	if open != 1 {
		t.Fatalf("open invitations after concurrent renewal = %d, want 1", open)
	}
	if client.User.GetX(ctx, u.ID).IsActive {
		t.Fatal("concurrent renewal activated user")
	}
}

func TestUserServiceDirectCreationCompletesOnboarding(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	userSvc := NewUserService(client)
	direct, err := userSvc.Create(ctx, UserCreateParams{
		CompanyID: invitationTestCompanyID,
		Name:      "Direct User",
		Email:     "direct@example.test",
		Password:  "Password1!",
		Role:      "tech",
	})
	if err != nil {
		t.Fatalf("create direct user: %v", err)
	}
	if direct.OnboardingCompletedAt == nil || !direct.IsActive {
		t.Fatal("direct-password user was not completed and active at creation")
	}
	if _, err := userSvc.Create(ctx, UserCreateParams{CompanyID: invitationTestCompanyID, Name: "Empty", Email: "empty@example.test", Role: "tech"}); err == nil {
		t.Fatal("direct user creation accepted empty password")
	}
}

func createInvitedUser(t *testing.T, ctx context.Context, client *ent.Client, companyID int64, name string) *ent.User {
	t.Helper()
	u, err := NewUserService(client).Create(ctx, UserCreateParams{
		CompanyID:        companyID,
		Name:             name,
		Email:            name + "@example.test",
		Role:             "tech",
		SendWelcomeEmail: true,
	})
	if err != nil {
		t.Fatalf("create invited user: %v", err)
	}
	return u
}

func createPendingInvitation(t *testing.T, ctx context.Context, client *ent.Client, svc *InvitationService, companyID int64, name string) (*ent.User, string) {
	t.Helper()
	u := createInvitedUser(t, ctx, client, companyID, name)
	token, err := svc.CreateInvite(ctx, companyID, u.ID)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	return u, token
}

func createUnownedInvite(t *testing.T, ctx context.Context, client *ent.Client, userID int64) string {
	t.Helper()
	token := fmt.Sprintf("legacy-%d-%d", userID, time.Now().UnixNano())
	client.InvitationToken.Create().
		SetTokenHash(tokenHash(token)).
		SetUserID(userID).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SaveX(ctx)
	return token
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
