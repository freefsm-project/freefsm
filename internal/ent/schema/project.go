package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Project struct {
	ent.Schema
}

func (Project) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "projects"},
	}
}

func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("customer_id"),
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.Int64("status_id").Optional().Nillable(),
		field.Int64("location_id").Optional().Nillable(),
		field.Float("completion_percentage").Default(0),
		field.Time("start_time").Optional().Nillable(),
		field.Time("end_time").Optional().Nillable(),
		field.String("notes").Default(""),
		field.String("custom_fields").Default("[]"),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Project) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
	}
}
