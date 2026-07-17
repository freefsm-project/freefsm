package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/comment"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

var (
	ErrCommentInvalid  = errors.New("invalid comment request")
	ErrCommentNotFound = errors.New("comment not found")
)

type CommentService struct {
	client  *ent.Client
	objects objectref.Directory
}

func NewCommentService(client *ent.Client, objects objectref.Directory) *CommentService {
	return &CommentService{client: client, objects: objects}
}

func (s *CommentService) ListForObject(ctx context.Context, companyID int64, ref objectref.Ref) ([]*ent.Comment, error) {
	if err := s.validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}
	comments, err := s.client.Comment.Query().
		Where(comment.CompanyIDEQ(companyID), comment.ObjectTypeEQ(ref.ObjectType()), comment.ObjectIDEQ(ref.ObjectID())).
		Order(ent.Desc(comment.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return comments, nil
}

func (s *CommentService) Create(ctx context.Context, companyID int64, ref objectref.Ref, authorID int64, content string) (*ent.Comment, error) {
	if err := s.validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if authorID <= 0 {
		return nil, fmt.Errorf("%w: author id must be positive: %d", ErrCommentInvalid, authorID)
	}
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsActive); err != nil {
		return nil, err
	}
	authorExists, err := s.client.User.Query().Where(user.IDEQ(authorID), user.CompanyIDEQ(companyID)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("validate comment author: %w", err)
	}
	if !authorExists {
		return nil, fmt.Errorf("%w: author %d", ErrCommentNotFound, authorID)
	}
	c, err := s.client.Comment.Create().
		SetCompanyID(companyID).
		SetObjectType(ref.ObjectType()).
		SetObjectID(ref.ObjectID()).
		SetAuthorID(authorID).
		SetContent(content).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create comment: %w", err)
	}
	return c, nil
}

func (s *CommentService) GetByID(ctx context.Context, companyID, id int64) (*ent.Comment, error) {
	if err := s.validateCompanyID(companyID); err != nil {
		return nil, err
	}
	if id <= 0 {
		return nil, fmt.Errorf("%w: comment id must be positive: %d", ErrCommentInvalid, id)
	}
	c, err := s.client.Comment.Query().Where(comment.IDEQ(id), comment.CompanyIDEQ(companyID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %d", ErrCommentNotFound, id)
		}
		return nil, fmt.Errorf("get comment %d: %w", id, err)
	}
	return c, nil
}

func (s *CommentService) Delete(ctx context.Context, companyID int64, ref objectref.Ref, id int64) error {
	if err := s.validateCompanyID(companyID); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: comment id must be positive: %d", ErrCommentInvalid, id)
	}
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsActive); err != nil {
		return err
	}

	deleted, err := s.client.Comment.Delete().Where(
		comment.IDEQ(id),
		comment.CompanyIDEQ(companyID),
		comment.ObjectTypeEQ(ref.ObjectType()),
		comment.ObjectIDEQ(ref.ObjectID()),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete comment %d: %w", id, err)
	}
	if deleted == 0 {
		return fmt.Errorf("%w: %d", ErrCommentNotFound, id)
	}
	return nil
}

func (s *CommentService) validateCompanyID(companyID int64) error {
	if companyID <= 0 {
		return fmt.Errorf("%w: company id must be positive: %d", ErrCommentInvalid, companyID)
	}
	return nil
}

func (s *CommentService) validateTarget(ctx context.Context, companyID int64, ref objectref.Ref, mode objectref.ExistenceMode) error {
	if !ref.Valid() {
		if _, err := objectref.Parse(ref.ObjectType(), ref.ObjectID()); err != nil {
			return fmt.Errorf("%w: %v", ErrCommentInvalid, err)
		}
	}
	if !objectref.Known(ref.Type) {
		return fmt.Errorf("%w: %s", objectref.ErrUnknownType, ref.Type)
	}
	if !s.objects.Supports(ref.Type, objectref.CapComments) {
		return fmt.Errorf("object type does not support comments: %s", ref.Type)
	}
	exists, err := s.objects.Exists(ctx, ref, mode)
	if err != nil {
		return fmt.Errorf("validate comment target: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: target %s %d", ErrCommentNotFound, ref.Type, ref.ID)
	}
	owner, err := s.objects.TargetCompanyID(ctx, ref)
	if err != nil {
		return fmt.Errorf("validate comment target ownership: %w", err)
	}
	if owner != companyID {
		return fmt.Errorf("%w: target %s %d", ErrCommentNotFound, ref.Type, ref.ID)
	}
	return nil
}
