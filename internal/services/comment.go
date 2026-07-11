package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/comment"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type CommentService struct {
	client  *ent.Client
	objects objectref.Directory
}

func NewCommentService(client *ent.Client, objects objectref.Directory) *CommentService {
	return &CommentService{client: client, objects: objects}
}

func (s *CommentService) ListForObject(ctx context.Context, ref objectref.Ref) ([]*ent.Comment, error) {
	if err := s.validateTarget(ctx, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}
	comments, err := s.client.Comment.Query().
		Where(comment.ObjectTypeEQ(ref.ObjectType()), comment.ObjectIDEQ(ref.ObjectID())).
		Order(ent.Desc(comment.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return comments, nil
}

func (s *CommentService) Create(ctx context.Context, ref objectref.Ref, authorID int64, content string) (*ent.Comment, error) {
	if err := s.validateTarget(ctx, ref, objectref.ExistsActive); err != nil {
		return nil, err
	}
	c, err := s.client.Comment.Create().
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

func (s *CommentService) GetByID(ctx context.Context, id int64) (*ent.Comment, error) {
	c, err := s.client.Comment.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get comment %d: %w", id, err)
	}
	return c, nil
}

func (s *CommentService) Delete(ctx context.Context, id int64) error {
	c, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	ref, err := objectref.Parse(c.ObjectType, c.ObjectID)
	if err != nil {
		return fmt.Errorf("invalid comment target: %w", err)
	}
	if err := s.validateTarget(ctx, ref, objectref.ExistsActive); err != nil {
		return err
	}

	if err := s.client.Comment.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete comment %d: %w", id, err)
	}
	return nil
}

func (s *CommentService) validateTarget(ctx context.Context, ref objectref.Ref, mode objectref.ExistenceMode) error {
	if !ref.Valid() {
		if _, err := objectref.Parse(ref.ObjectType(), ref.ObjectID()); err != nil {
			return err
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
		if mode == objectref.ExistsActive {
			return fmt.Errorf("comment target not found or archived: %s %d", ref.Type, ref.ID)
		}
		return fmt.Errorf("comment target not found: %s %d", ref.Type, ref.ID)
	}
	return nil
}
