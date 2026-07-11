package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customfielddefinition"
)

type CustomFieldDefinitionService struct {
	client *ent.Client
}

func NewCustomFieldDefinitionService(client *ent.Client) *CustomFieldDefinitionService {
	return &CustomFieldDefinitionService{client: client}
}

func (s *CustomFieldDefinitionService) ListForObjectType(ctx context.Context, objectType string) ([]*ent.CustomFieldDefinition, error) {
	defs, err := s.client.CustomFieldDefinition.Query().
		Where(customfielddefinition.ObjectTypeEQ(objectType)).
		Order(ent.Asc(customfielddefinition.FieldSortOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list custom field definitions for %s: %w", objectType, err)
	}
	return defs, nil
}

func (s *CustomFieldDefinitionService) ListAll(ctx context.Context) ([]*ent.CustomFieldDefinition, error) {
	defs, err := s.client.CustomFieldDefinition.Query().
		Order(ent.Asc(customfielddefinition.FieldObjectType), ent.Asc(customfielddefinition.FieldSortOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all custom field definitions: %w", err)
	}
	return defs, nil
}

type CustomFieldDefCreateParams struct {
	ObjectType string
	Name       string
	FieldType  string
	Required   bool
	Options    string
	SortOrder  int
}

func (s *CustomFieldDefinitionService) Create(ctx context.Context, p CustomFieldDefCreateParams) (*ent.CustomFieldDefinition, error) {
	d, err := s.client.CustomFieldDefinition.Create().
		SetObjectType(p.ObjectType).
		SetName(p.Name).
		SetFieldType(p.FieldType).
		SetRequired(p.Required).
		SetOptions(p.Options).
		SetSortOrder(p.SortOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create custom field definition: %w", err)
	}
	return d, nil
}

type CustomFieldDefUpdateParams struct {
	Name      *string
	FieldType *string
	Required  *bool
	Options   *string
	SortOrder *int
}

func (s *CustomFieldDefinitionService) Update(ctx context.Context, id int64, p CustomFieldDefUpdateParams) (*ent.CustomFieldDefinition, error) {
	u := s.client.CustomFieldDefinition.UpdateOneID(id)
	if p.Name != nil {
		u.SetName(*p.Name)
	}
	if p.FieldType != nil {
		u.SetFieldType(*p.FieldType)
	}
	if p.Required != nil {
		u.SetRequired(*p.Required)
	}
	if p.Options != nil {
		u.SetOptions(*p.Options)
	}
	if p.SortOrder != nil {
		u.SetSortOrder(*p.SortOrder)
	}
	d, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update custom field definition %d: %w", id, err)
	}
	return d, nil
}

func (s *CustomFieldDefinitionService) Delete(ctx context.Context, id int64) error {
	if err := s.client.CustomFieldDefinition.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete custom field definition %d: %w", id, err)
	}
	return nil
}

var CustomFieldObjectTypes = []string{"customer", "job", "project", "estimate", "invoice", "asset"}
var CustomFieldTypes = []string{"text", "number", "date", "textarea", "select", "checkbox"}
