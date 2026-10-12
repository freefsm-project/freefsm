package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type AssetType struct {
	ent.Schema
}

func (AssetType) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "asset_types"},
	}
}

func (AssetType) Fields() []ent.Field {
	return []ent.Field{
		field.Bool("retired").Default(false),
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.Int("sort_order").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetType) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("company_id"),
	}
}
