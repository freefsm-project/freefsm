package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type JobAssignment struct {
	ent.Schema
}

func (JobAssignment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "job_assignments"},
	}
}

func (JobAssignment) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("job_id"),
		field.Int64("user_id"),
		field.String("role").Default(""),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (JobAssignment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id"),
		index.Fields("user_id"),
		index.Fields("job_id", "user_id").Unique(),
	}
}
