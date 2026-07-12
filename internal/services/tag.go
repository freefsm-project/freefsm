package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/tag"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrTagInvalid  = errors.New("invalid tag request")
	ErrTagNotFound = errors.New("tag not found")
	ErrTagConflict = errors.New("tag conflict")
)

type TagService struct {
	client *ent.Client
}

func NewTagService(client *ent.Client) *TagService {
	return &TagService{client: client}
}

func (s *TagService) ListAll(ctx context.Context, companyID int64) ([]*ent.Tag, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	return s.client.Tag.Query().Where(tag.CompanyIDEQ(companyID)).Order(ent.Asc(tag.FieldName)).All(ctx)
}

func (s *TagService) GetByID(ctx context.Context, companyID, id int64) (*ent.Tag, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	t, err := s.client.Tag.Query().Where(tag.IDEQ(id), tag.CompanyIDEQ(companyID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %d", ErrTagNotFound, id)
		}
		return nil, fmt.Errorf("get tag %d: %w", id, err)
	}
	return t, nil
}

func (s *TagService) Create(ctx context.Context, companyID int64, name, color string) (*ent.Tag, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrTagInvalid)
	}
	if color == "" {
		color = "#3B82F6"
	}
	t, err := s.client.Tag.Create().
		SetCompanyID(companyID).
		SetName(name).
		SetColor(color).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

func (s *TagService) Update(ctx context.Context, companyID, id int64, name, color string) (*ent.Tag, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	existing, err := s.GetByID(ctx, companyID, id)
	if err != nil {
		return nil, err
	}
	b := existing.Update()
	if name != "" {
		b.SetName(name)
	}
	if color != "" {
		b.SetColor(color)
	}
	t, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update tag %d: %w", id, err)
	}
	return t, nil
}

func (s *TagService) Delete(ctx context.Context, companyID, id int64) error {
	if err := validateCompanyID(companyID); err != nil {
		return err
	}
	n, err := s.client.Tag.Delete().Where(tag.IDEQ(id), tag.CompanyIDEQ(companyID)).Exec(ctx)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001") {
			return fmt.Errorf("%w: tag is linked", ErrTagConflict)
		}
		return fmt.Errorf("delete tag %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %d", ErrTagNotFound, id)
	}
	return nil
}

func validateCompanyID(companyID int64) error {
	if companyID <= 0 {
		return fmt.Errorf("%w: company id must be positive: %d", ErrTagInvalid, companyID)
	}
	return nil
}
