package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Customer struct {
	ent.Schema
}

func (Customer) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "customers"},
	}
}

func (Customer) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("first_name").Default(""),
		field.String("last_name").Default(""),
		field.String("display_name").NotEmpty(),
		field.String("email").Default(""),
		field.String("phone").Default(""),
		field.String("company_name").Default(""),
		field.String("notes").Default(""),
		field.String("status").Default("lead"),
		field.String("account_type").Default("individual"),
		field.Int64("assigned_to").Optional().Nillable(),
		field.Int64("pipeline_status_id").Optional().Nillable(),
		field.Int64("lead_source_id").Optional().Nillable(),
		field.String("billing_address_1").Default(""),
		field.String("billing_address_2").Default(""),
		field.String("billing_city").Default(""),
		field.String("billing_state").Default(""),
		field.String("billing_zip_code").Default(""),
		field.Bool("tax_exempt").Default(false),
		field.String("custom_fields").Default("[]"),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Customer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("display_name"),
		index.Fields("email"),
		index.Fields("phone"),
		index.Fields("status"),
	}
}
