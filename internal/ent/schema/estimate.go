package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Estimate struct {
	ent.Schema
}

func (Estimate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "estimates"},
	}
}

func (Estimate) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("customer_id").Optional().Nillable(),
		field.Int64("job_id").Optional().Nillable(),
		field.Int64("status_id").Optional().Nillable(),
		field.String("title").Default(""),
		field.String("notes").Default(""),
		field.String("tax_rate").Default("0"),
		field.String("line_items").Default("[]"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Estimate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
		index.Fields("job_id"),
	}
}
