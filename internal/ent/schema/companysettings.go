package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type CompanySettings struct {
	ent.Schema
}

func (CompanySettings) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "company_settings"},
	}
}

func (CompanySettings) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("business_name").Default(""),
		field.String("address").Default(""),
		field.String("city").Default(""),
		field.String("state").Default(""),
		field.String("zip").Default(""),
		field.String("phone").Default(""),
		field.String("email").Default(""),
		field.String("tax_id").Default(""),
		field.String("default_tax_rate").Default("0"),
		field.String("invoice_prefix").Default("INV-"),
		field.String("estimate_prefix").Default("EST-"),
		field.Int("default_due_days").Default(30),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
