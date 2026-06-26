package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type DashboardLayout struct {
	ent.Schema
}

func (DashboardLayout) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "dashboard_layouts"},
	}
}

func (DashboardLayout) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("user_id").Optional().Nillable(),
		field.String("scope").Default("user"),
		field.String("name").Default(""),
		field.Bool("is_default").Default(false),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DashboardLayout) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "scope").Unique(),
		index.Fields("company_id", "scope", "is_default"),
	}
}
