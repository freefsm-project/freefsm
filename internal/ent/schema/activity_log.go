package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ActivityLog struct {
	ent.Schema
}

func (ActivityLog) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "activity_logs"},
	}
}

func (ActivityLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id"),
		field.Int64("actor_id"),
		field.String("action").NotEmpty(),
		field.String("object_type").NotEmpty(),
		field.Int64("object_id"),
		field.String("metadata").Default("{}"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ActivityLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "object_id"),
		index.Fields("created_at"),
	}
}
