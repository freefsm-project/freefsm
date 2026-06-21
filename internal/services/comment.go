package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/comment"
)

type CommentService struct {
	client *ent.Client
}

func NewCommentService(client *ent.Client) *CommentService {
	return &CommentService{client: client}
}

func (s *CommentService) ListForObject(ctx context.Context, objectType string, objectID int64) ([]*ent.Comment, error) {
	comments, err := s.client.Comment.Query().
		Where(comment.ObjectTypeEQ(objectType), comment.ObjectIDEQ(objectID)).
		Order(ent.Desc(comment.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return comments, nil
}

func (s *CommentService) Create(ctx context.Context, objectType string, objectID int64, authorID int64, content string) (*ent.Comment, error) {
	c, err := s.client.Comment.Create().
		SetObjectType(objectType).
		SetObjectID(objectID).
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
