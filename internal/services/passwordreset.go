package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/passwordresettoken"
)

type PasswordResetService struct {
	client *ent.Client
}

func NewPasswordResetService(client *ent.Client) *PasswordResetService {
	return &PasswordResetService{client: client}
}

func (s *PasswordResetService) CreateToken(ctx context.Context, userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	token := hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))

	_, err := s.client.PasswordResetToken.Create().
		SetTokenHash(hex.EncodeToString(hash[:])).
		SetUserID(userID).
		SetExpiresAt(time.Now().Add(1 * time.Hour)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("create reset token: %w", err)
	}
	return token, nil
}

func (s *PasswordResetService) Validate(ctx context.Context, token string) (int64, error) {
	hash := sha256.Sum256([]byte(token))
	t, err := s.client.PasswordResetToken.Query().
		Where(
			passwordresettoken.TokenHashEQ(hex.EncodeToString(hash[:])),
			passwordresettoken.ExpiresAtGT(time.Now()),
		).
		Only(ctx)
	if err != nil {
		return 0, fmt.Errorf("invalid or expired token")
	}
	return t.UserID, nil
}

func (s *PasswordResetService) Consume(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.client.PasswordResetToken.Delete().
		Where(passwordresettoken.TokenHashEQ(hex.EncodeToString(hash[:]))).Exec(ctx)
	return err
}
