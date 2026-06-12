package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CustomerContact struct {
	ent.Schema
}

func (CustomerContact) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "customer_contacts"},
	}
}

func (CustomerContact) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("customer_id"),
		field.String("first_name").Default(""),
		field.String("last_name").Default(""),
		field.String("email").Default(""),
		field.String("phone").Default(""),
		field.String("notes").Default(""),
		field.Int("sort_order").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (CustomerContact) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
	}
}
