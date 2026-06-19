package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
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
		field.Int64("company_id").Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.String("object_type").NotEmpty(),
		field.Time("created_at").Default(time.Now),
	}
}

func (StatusWorkflow) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("statuses", Status.Type),
	}
}
