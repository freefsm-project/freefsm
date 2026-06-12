package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Status struct {
	ent.Schema
}

func (Status) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "statuses"},
	}
}

func (Status) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("workflow_id"),
		field.String("name").NotEmpty(),
		field.String("color").Default("#6B7280"),
		field.Int("sort_order").Default(0),
		field.Time("created_at"),
	}
}

func (Status) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("workflow", StatusWorkflow.Type).
			Ref("statuses").
			Field("workflow_id").
			Unique().
			Required(),
	}
}

func (Status) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("workflow_id"),
	}
}
