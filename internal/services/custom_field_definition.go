package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customfielddefinition"
)

var ErrIncompatibleConversionField = errors.New("conversion key counterpart must use the same field type and compatible options")
var ErrDuplicateConversionKey = errors.New("conversion key is already used by another field of this entity type")

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
	ObjectType    string
	Name          string
	FieldType     string
	Required      bool
	Options       string
	SortOrder     int
	ConversionKey string
}

func (s *CustomFieldDefinitionService) Create(ctx context.Context, p CustomFieldDefCreateParams) (*ent.CustomFieldDefinition, error) {
	b := s.client.CustomFieldDefinition.Create().
		SetObjectType(p.ObjectType).
		SetName(p.Name).
		SetFieldType(p.FieldType).
		SetRequired(p.Required).
		SetOptions(p.Options).
		SetSortOrder(p.SortOrder)
	key, err := validateConversionKey(p.ObjectType, p.ConversionKey)
	if err != nil {
		return nil, err
	}
	if key != "" {
		if err := s.validateUniqueKey(ctx, 0, p.ObjectType, key); err != nil {
			return nil, err
		}
		if err := s.validateCounterpart(ctx, 0, p.ObjectType, key, p.FieldType, p.Options); err != nil {
			return nil, err
		}
		b.SetConversionKey(key)
	}
	d, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create custom field definition: %w", err)
	}
	return d, nil
}

type CustomFieldDefUpdateParams struct {
	Name          *string
	FieldType     *string
	Required      *bool
	Options       *string
	SortOrder     *int
	ConversionKey *string
}

func (s *CustomFieldDefinitionService) Update(ctx context.Context, id int64, p CustomFieldDefUpdateParams) (*ent.CustomFieldDefinition, error) {
	current, err := s.client.CustomFieldDefinition.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get custom field definition %d: %w", id, err)
	}
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
	key := stringValue(current.ConversionKey)
	if p.ConversionKey != nil {
		key, err = validateConversionKey(current.ObjectType, *p.ConversionKey)
		if err != nil {
			return nil, err
		}
		if key == "" {
			u.ClearConversionKey()
		} else {
			u.SetConversionKey(key)
		}
	}
	if key != "" {
		fieldType, options := current.FieldType, current.Options
		if p.FieldType != nil {
			fieldType = *p.FieldType
		}
		if p.Options != nil {
			options = *p.Options
		}
		if err := s.validateUniqueKey(ctx, id, current.ObjectType, key); err != nil {
			return nil, err
		}
		if err := s.validateCounterpart(ctx, id, current.ObjectType, key, fieldType, options); err != nil {
			return nil, err
		}
	}
	d, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update custom field definition %d: %w", id, err)
	}
	return d, nil
}

func (s *CustomFieldDefinitionService) validateUniqueKey(ctx context.Context, excludeID int64, objectType, key string) error {
	q := s.client.CustomFieldDefinition.Query().Where(customfielddefinition.ObjectTypeEQ(objectType), customfielddefinition.ConversionKeyEQ(key))
	if excludeID > 0 {
		q = q.Where(customfielddefinition.IDNEQ(excludeID))
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("check conversion key uniqueness: %w", err)
	}
	if exists {
		return ErrDuplicateConversionKey
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *CustomFieldDefinitionService) validateCounterpart(ctx context.Context, excludeID int64, objectType, key, fieldType, options string) error {
	counterpart := "estimate"
	if objectType == "estimate" {
		counterpart = "invoice"
	}
	q := s.client.CustomFieldDefinition.Query().Where(customfielddefinition.ObjectTypeEQ(counterpart), customfielddefinition.ConversionKeyEQ(key))
	if excludeID > 0 {
		q = q.Where(customfielddefinition.IDNEQ(excludeID))
	}
	other, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check conversion key counterpart: %w", err)
	}
	if other.FieldType != fieldType || !compatibleFieldOptions(fieldType, options, other.Options) {
		return ErrIncompatibleConversionField
	}
	return nil
}

func compatibleFieldOptions(fieldType, left, right string) bool {
	if fieldType != "select" && fieldType != "checkbox" {
		return true
	}
	var a, b []string
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *CustomFieldDefinitionService) Delete(ctx context.Context, id int64) error {
	if err := s.client.CustomFieldDefinition.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete custom field definition %d: %w", id, err)
	}
	return nil
}

var CustomFieldObjectTypes = []string{"customer", "job", "project", "estimate", "invoice", "asset"}
var CustomFieldTypes = []string{"text", "number", "date", "textarea", "select", "checkbox"}
var conversionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func validateConversionKey(objectType, raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}
	if objectType != "estimate" && objectType != "invoice" {
		return "", fmt.Errorf("conversion key is only valid for estimate and invoice fields")
	}
	if !conversionKeyPattern.MatchString(key) {
		return "", fmt.Errorf("conversion key must contain only letters, numbers, underscores, or hyphens")
	}
	return key, nil
}
