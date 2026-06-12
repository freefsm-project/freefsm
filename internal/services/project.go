package services

import (
	"context"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/project"
)

type ProjectService struct {
	client *ent.Client
}

func NewProjectService(client *ent.Client) *ProjectService {
	return &ProjectService{client: client}
}

func (s *ProjectService) ListAll(ctx context.Context) ([]*ent.Project, error) {
	return s.client.Project.Query().Order(ent.Asc(project.FieldName)).All(ctx)
}
