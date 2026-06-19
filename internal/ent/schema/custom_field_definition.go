package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CustomFieldDefinition struct {
	ent.Schema
}

func (CustomFieldDefinition) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "custom_field_definitions"},
	}
}

func (CustomFieldDefinition) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("object_type").NotEmpty(),
		field.String("name").NotEmpty(),
		field.String("field_type").NotEmpty(),
		field.Bool("required").Default(false),
		field.String("options").Default("[]"),
		field.Int("sort_order").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (CustomFieldDefinition) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "sort_order"),
	}
}
