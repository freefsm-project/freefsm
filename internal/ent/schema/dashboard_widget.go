package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type DashboardWidget struct {
	ent.Schema
}

func (DashboardWidget) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "dashboard_widgets"},
	}
}

func (DashboardWidget) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("layout_id"),
		field.String("widget_type"),
		field.String("title").Default(""),
		field.Int("position").Default(0),
		field.Bool("hidden").Default(false),
		field.String("config").Default("{}"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DashboardWidget) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("layout_id", "position"),
		index.Fields("layout_id", "widget_type").Unique(),
	}
}
