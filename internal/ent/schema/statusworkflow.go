package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type StatusWorkflow struct {
	ent.Schema
}

func (StatusWorkflow) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "status_workflows"},
	}
}

func (StatusWorkflow) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("name").NotEmpty(),
		field.String("object_type").NotEmpty(),
		field.Time("created_at"),
	}
}

func (StatusWorkflow) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type"),
	}
}
