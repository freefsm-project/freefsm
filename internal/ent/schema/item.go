package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Item struct {
	ent.Schema
}

func (Item) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "items"},
	}
}

func (Item) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.String("type").Default("service"),
		field.String("sku").Default(""),
		field.Float("unit_price").Default(0),
		field.Float("unit_cost").Default(0),
		field.Bool("taxable").Default(true),
		field.String("tax_rate").Default(""),
		field.Bool("track_inventory").Default(false),
		field.String("description").Default(""),
		field.Bool("is_active").Default(true),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Item) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}
