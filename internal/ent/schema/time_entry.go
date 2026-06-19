package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TimeEntry struct {
	ent.Schema
}

func (TimeEntry) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "time_entries"},
	}
}

func (TimeEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("user_id"),
		field.Bool("is_manual").Default(false),
		field.Time("clock_in"),
		field.Time("clock_out").Optional().Nillable(),
		field.Float("latitude").Optional().Nillable(),
		field.Float("longitude").Optional().Nillable(),
		field.String("notes").Default(""),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TimeEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "clock_out"),
		index.Fields("clock_in"),
	}
}
