package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Asset struct {
	ent.Schema
}

func (Asset) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "assets"},
	}
}

func (Asset) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("customer_id"),
		field.Int64("location_id").Optional().Nillable(),
		field.Int64("asset_type_id"),
		field.Int64("asset_status_id").Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.String("serial_number").Default(""),
		field.String("model").Default(""),
		field.String("manufacturer").Default(""),
		field.String("notes").Default(""),
		field.Time("installed_at").Optional().Nillable(),
		field.Time("warranty_expires").Optional().Nillable(),
		field.String("custom_fields").Default("[]"),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Asset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
		index.Fields("location_id"),
		index.Fields("asset_type_id"),
		index.Fields("asset_status_id"),
		index.Fields("company_id"),
	}
}
