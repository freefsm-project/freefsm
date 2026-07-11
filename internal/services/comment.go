package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/comment"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type CommentService struct {
	client *ent.Client
}

func NewCommentService(client *ent.Client) *CommentService {
	return &CommentService{client: client}
}

func (s *CommentService) ListForObject(ctx context.Context, ref objectref.Ref) ([]*ent.Comment, error) {
	if !ref.Valid() {
		return nil, fmt.Errorf("invalid comment target: %s %d", ref.Type, ref.ID)
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
	if !ref.Valid() {
		return nil, fmt.Errorf("invalid comment target: %s %d", ref.Type, ref.ID)
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
	if err := s.client.Comment.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete comment %d: %w", id, err)
	}
	return nil
}
