package services

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"golang.org/x/crypto/bcrypt"
)

type UserService struct {
	client *ent.Client
}

func NewUserService(client *ent.Client) *UserService {
	return &UserService{client: client}
}

func (s *UserService) GetByID(ctx context.Context, id int64) (*ent.User, error) {
	u, err := s.client.User.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", id, err)
	}
	return u, nil
}

func (s *UserService) GetByEmail(ctx context.Context, email string) (*ent.User, error) {
	u, err := s.client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

func (s *UserService) ListAll(ctx context.Context) ([]*ent.User, error) {
	return s.client.User.Query().Order(ent.Asc(user.FieldName)).All(ctx)
}

type UserCreateParams struct {
	Name             string
	Email            string
	Password         string
	Role             string
	SendWelcomeEmail bool
}

func (s *UserService) Create(ctx context.Context, p UserCreateParams) (*ent.User, error) {
	password := p.Password
	active := true

	if p.SendWelcomeEmail {
		password = generateUnusablePassword()
		active = false
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	u, err := s.client.User.Create().
		SetName(p.Name).
		SetEmail(p.Email).
		SetPasswordHash(string(hash)).
		SetRole(p.Role).
		SetIsActive(active).
		SetForcePasswordChange(false).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	if p.SendWelcomeEmail {
		_, err = s.client.User.UpdateOne(u).
			SetWelcomeEmailSentAt(time.Now()).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("update welcome sent at: %w", err)
		}
	}

	return u, nil
}

type UserUpdateParams struct {
	Name     *string
	Email    *string
	Role     *string
	Password *string
}

func (s *UserService) Update(ctx context.Context, id int64, p UserUpdateParams) (*ent.User, error) {
	u := s.client.User.UpdateOneID(id)
	if p.Name != nil {
		u.SetName(*p.Name)
	}
	if p.Email != nil {
		u.SetEmail(*p.Email)
	}
	if p.Role != nil {
		u.SetRole(*p.Role)
	}
	if p.Password != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*p.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		u.SetPasswordHash(string(hash))
	}
	ret, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	return ret, nil
}

func (s *UserService) SetActive(ctx context.Context, id int64, active bool) error {
	return s.client.User.UpdateOneID(id).SetIsActive(active).Exec(ctx)
}

func (s *UserService) ActivateWithPassword(ctx context.Context, id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.client.User.UpdateOneID(id).
		SetPasswordHash(string(hash)).
		SetIsActive(true).
		SetForcePasswordChange(false).
		Exec(ctx)
}

func (s *UserService) SetPassword(ctx context.Context, id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.client.User.UpdateOneID(id).SetPasswordHash(string(hash)).Exec(ctx)
}

func (s *UserService) UpdateFontSize(ctx context.Context, id int64, fontSize string) error {
	if !ValidFontSize(fontSize) {
		return fmt.Errorf("invalid font size")
	}
	return s.client.User.UpdateOneID(id).SetFontSize(fontSize).Exec(ctx)
}

func (s *UserService) UpdateSchedulePreferences(ctx context.Context, id int64, tab, period string) error {
	if !ValidScheduleTab(tab) {
		return fmt.Errorf("invalid schedule tab")
	}
	if !ValidSchedulePeriod(period) {
		return fmt.Errorf("invalid schedule period")
	}
	return s.client.User.UpdateOneID(id).
		SetLastScheduleTab(tab).
		SetLastSchedulePeriod(period).
		Exec(ctx)
}

func ValidFontSize(fontSize string) bool {
	switch fontSize {
	case "small", "medium", "large":
		return true
	default:
		return false
	}
}

func ValidScheduleTab(tab string) bool {
	switch tab {
	case "list", "calendar", "dispatch", "map":
		return true
	default:
		return false
	}
}

func ValidSchedulePeriod(period string) bool {
	switch period {
	case "month", "week", "day":
		return true
	default:
		return false
	}
}

func (s *UserService) Authenticate(ctx context.Context, email, password string) error {
	u, err := s.client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
	if err != nil {
		return fmt.Errorf("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return fmt.Errorf("invalid credentials")
	}
	return nil
}

func (s *UserService) ClearForcePasswordChange(ctx context.Context, id int64) error {
	return s.client.User.UpdateOneID(id).SetForcePasswordChange(false).Exec(ctx)
}

func (s *UserService) MustChangePassword(ctx context.Context, userID int64) (bool, error) {
	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		return false, err
	}
	return u.ForcePasswordChange, nil
}

func (s *UserService) ValidatePassword(password string, cs *ent.CompanySettings) error {
	if cs == nil {
		cs = &ent.CompanySettings{PasswordMinLength: 8, PasswordRequireUppercase: true, PasswordRequireLowercase: true, PasswordRequireDigit: true, PasswordRequireSpecial: true}
	}
	if len(password) < cs.PasswordMinLength {
		return fmt.Errorf("password must be at least %d characters", cs.PasswordMinLength)
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	hasSpecial := false
	for _, r := range password {
		if unicode.IsUpper(r) {
			hasUpper = true
		}
		if unicode.IsLower(r) {
			hasLower = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			hasSpecial = true
		}
	}
	if cs.PasswordRequireUppercase && !hasUpper {
		return fmt.Errorf("password must contain an uppercase letter")
	}
	if cs.PasswordRequireLowercase && !hasLower {
		return fmt.Errorf("password must contain a lowercase letter")
	}
	if cs.PasswordRequireDigit && !hasDigit {
		return fmt.Errorf("password must contain a digit")
	}
	if cs.PasswordRequireSpecial && !hasSpecial {
		return fmt.Errorf("password must contain a special character")
	}
	return nil
}

func (s *UserService) ResendWelcomeEmail(ctx context.Context, id int64) (*ent.User, error) {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	_, err = s.client.User.UpdateOne(u).
		SetIsActive(false).
		SetForcePasswordChange(false).
		SetWelcomeEmailSentAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	return u, nil
}

func generateUnusablePassword() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var sb strings.Builder
	for i := 1; i <= 64; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		sb.WriteByte(chars[n.Int64()])
	}
	return sb.String()
}
