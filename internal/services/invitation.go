package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/invitationtoken"
)

type InvitationService struct {
	client *ent.Client
}

func NewInvitationService(client *ent.Client) *InvitationService {
	return &InvitationService{client: client}
}

func (s *InvitationService) CreateInvite(ctx context.Context, userID int64) (string, error) {
	if err := s.InvalidateOpenInvites(ctx, userID); err != nil {
		return "", err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	token := hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))

	_, err := s.client.InvitationToken.Create().
		SetTokenHash(hex.EncodeToString(hash[:])).
		SetUserID(userID).
		SetExpiresAt(time.Now().Add(72 * time.Hour)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("create invitation token: %w", err)
	}
	return token, nil
}

func (s *InvitationService) ValidateInvite(ctx context.Context, token string) (int64, error) {
	hash := sha256.Sum256([]byte(token))
	t, err := s.client.InvitationToken.Query().
		Where(
			invitationtoken.TokenHashEQ(hex.EncodeToString(hash[:])),
			invitationtoken.ExpiresAtGT(time.Now()),
			invitationtoken.ConsumedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		return 0, fmt.Errorf("invalid or expired invitation")
	}
	return t.UserID, nil
}

func (s *InvitationService) ConsumeInvite(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.client.InvitationToken.Update().
		Where(
			invitationtoken.TokenHashEQ(hex.EncodeToString(hash[:])),
			invitationtoken.ConsumedAtIsNil(),
		).
		SetConsumedAt(time.Now()).
		Save(ctx)
	return err
}

func (s *InvitationService) InvalidateOpenInvites(ctx context.Context, userID int64) error {
	_, err := s.client.InvitationToken.Update().
		Where(invitationtoken.UserIDEQ(userID), invitationtoken.ConsumedAtIsNil()).
		SetConsumedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("invalidate invitations: %w", err)
	}
	return nil
}
