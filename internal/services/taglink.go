package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/tag"
	"github.com/freefsm-project/freefsm/internal/ent/taglink"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5/pgconn"
)

type TagLinkService struct {
	client  *ent.Client
	objects objectref.Directory
}

func NewTagLinkService(client *ent.Client, objects objectref.Directory) *TagLinkService {
	return &TagLinkService{client: client, objects: objects}
}

func (s *TagLinkService) Attach(ctx context.Context, companyID, tagID int64, ref objectref.Ref) (*ent.TagLink, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if err := s.validateRef(ctx, companyID, ref, objectref.ExistsActive); err != nil {
		return nil, err
	}
	if err := s.validateTag(ctx, companyID, tagID); err != nil {
		return nil, err
	}

	// Check if link already exists
	exists, err := s.client.TagLink.Query().
		Where(taglink.CompanyIDEQ(companyID), taglink.TagIDEQ(tagID), taglink.ObjectTypeEQ(ref.ObjectType()), taglink.ObjectIDEQ(ref.ObjectID())).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("check tag link: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("%w: tag already attached", ErrTagConflict)
	}

	l, err := s.client.TagLink.Create().
		SetCompanyID(companyID).
		SetTagID(tagID).
		SetObjectType(ref.ObjectType()).
		SetObjectID(ref.ObjectID()).
		Save(ctx)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: tag already attached", ErrTagConflict)
		}
		return nil, fmt.Errorf("attach tag: %w", err)
	}
	return l, nil
}

func (s *TagLinkService) Detach(ctx context.Context, companyID, tagID int64, ref objectref.Ref) error {
	if err := validateCompanyID(companyID); err != nil {
		return err
	}
	if err := s.validateRef(ctx, companyID, ref, objectref.ExistsActive); err != nil {
		return err
	}
	if err := s.validateTag(ctx, companyID, tagID); err != nil {
		return err
	}

	_, err := s.client.TagLink.Delete().
		Where(taglink.CompanyIDEQ(companyID), taglink.TagIDEQ(tagID), taglink.ObjectTypeEQ(ref.ObjectType()), taglink.ObjectIDEQ(ref.ObjectID())).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	return nil
}

func (s *TagLinkService) ListForObject(ctx context.Context, companyID int64, ref objectref.Ref) ([]*ent.Tag, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if err := s.validateRef(ctx, companyID, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}

	links, err := s.client.TagLink.Query().
		Where(taglink.CompanyIDEQ(companyID), taglink.ObjectTypeEQ(ref.ObjectType()), taglink.ObjectIDEQ(ref.ObjectID())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags for object: %w", err)
	}

	if len(links) == 0 {
		return nil, nil
	}

	tagIDs := make([]int64, len(links))
	for i, l := range links {
		tagIDs[i] = l.TagID
	}

	tags, err := s.client.Tag.Query().
		Where(tag.CompanyIDEQ(companyID), tag.IDIn(tagIDs...)).
		Order(ent.Asc(tag.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch tags: %w", err)
	}
	return tags, nil
}

func (s *TagLinkService) ListObjectsWithTag(ctx context.Context, companyID, tagID int64, typ objectref.Type) ([]*ent.TagLink, error) {
	if err := validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if err := s.validateType(typ); err != nil {
		return nil, err
	}
	if err := s.validateTag(ctx, companyID, tagID); err != nil {
		return nil, err
	}

	return s.client.TagLink.Query().
		Where(taglink.CompanyIDEQ(companyID), taglink.TagIDEQ(tagID), taglink.ObjectTypeEQ(string(typ))).
		All(ctx)
}

func (s *TagLinkService) validateRef(ctx context.Context, companyID int64, ref objectref.Ref, mode objectref.ExistenceMode) error {
	if !ref.Valid() {
		if _, err := objectref.Parse(ref.ObjectType(), ref.ObjectID()); err != nil {
			return fmt.Errorf("%w: %v", ErrTagInvalid, err)
		}
	}
	if err := s.validateType(ref.Type); err != nil {
		return err
	}
	exists, err := s.objects.Exists(ctx, ref, mode)
	if err != nil {
		return fmt.Errorf("validate tag target: %w", err)
	}
	if !exists {
		if mode == objectref.ExistsActive {
			return fmt.Errorf("%w: target not found or archived: %s %d", ErrTagNotFound, ref.Type, ref.ID)
		}
		return fmt.Errorf("%w: target %s %d", ErrTagNotFound, ref.Type, ref.ID)
	}
	owner, err := s.objects.TargetCompanyID(ctx, ref)
	if err != nil {
		return fmt.Errorf("validate tag target ownership: %w", err)
	}
	if owner != companyID {
		return fmt.Errorf("%w: target %s %d", ErrTagNotFound, ref.Type, ref.ID)
	}
	return nil
}

func (s *TagLinkService) validateType(typ objectref.Type) error {
	if !objectref.Known(typ) {
		return fmt.Errorf("%w: %s", objectref.ErrUnknownType, typ)
	}
	if !s.objects.Supports(typ, objectref.CapTags) {
		return fmt.Errorf("object type does not support tags: %s", typ)
	}
	return nil
}

func (s *TagLinkService) validateTag(ctx context.Context, companyID, tagID int64) error {
	if tagID <= 0 {
		return fmt.Errorf("%w: tag id must be positive: %d", ErrTagInvalid, tagID)
	}
	exists, err := s.client.Tag.Query().Where(tag.IDEQ(tagID), tag.CompanyIDEQ(companyID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate tag: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: %d", ErrTagNotFound, tagID)
	}
	return nil
}
