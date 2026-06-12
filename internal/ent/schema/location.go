package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Location struct {
	ent.Schema
}

func (Location) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "locations"},
	}
}

func (Location) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("object_type"),
		field.Int64("object_id"),
		field.String("title"),
		field.String("address_1").Default(""),
		field.String("address_2").Default(""),
		field.String("city").Default(""),
		field.String("state").Default(""),
		field.String("zip_code").Default(""),
		field.String("notes").Default(""),
		field.Bool("is_primary").Default(false),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Location) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "object_id"),
	}
}
