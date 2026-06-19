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
		field.Int64("company_id").Optional().Nillable(),
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
		field.String("smtp_host").Default(""),
		field.Int("smtp_port").Default(587),
		field.String("smtp_user").Default(""),
		field.String("smtp_password").Default(""),
		field.String("smtp_from").Default(""),
		field.String("timezone").Default("UTC"),
		field.Int("password_min_length").Default(8),
		field.Bool("password_require_uppercase").Default(true),
		field.Bool("password_require_lowercase").Default(true),
		field.Bool("password_require_digit").Default(true),
		field.Bool("password_require_special").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
