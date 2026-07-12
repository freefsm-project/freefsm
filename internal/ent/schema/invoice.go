package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Invoice struct {
	ent.Schema
}

func (Invoice) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "invoices"},
	}
}

func (Invoice) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("invoice_number").Optional(),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("customer_id"),
		field.Int64("job_id").Optional().Nillable(),
		field.Int64("estimate_id").Optional().Nillable(),
		field.Int64("status_id").Optional().Nillable(),
		field.String("title").Default(""),
		field.String("notes").Default(""),
		field.Time("invoice_date"),
		field.Time("due_date"),
		field.String("tax_rate").Default("0"),
		field.String("line_items").Default("[]"),
		field.String("settlement_state").Default("unpaid"),
		field.String("display_settings").Default("{}"),
		field.String("custom_fields").Default("[]"),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("conversion_hidden_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Invoice) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
		index.Fields("job_id"),
		index.Fields("status_id"),
		index.Fields("company_id", "invoice_number").Unique(),
	}
}
