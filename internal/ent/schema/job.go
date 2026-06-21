package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Job struct {
	ent.Schema
}

func (Job) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "jobs"},
	}
}

func (Job) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("customer_id"),
		field.Int64("project_id").Optional().Nillable(),
		field.Int64("location_id").Optional().Nillable(),
		field.Int64("customer_contact_id").Optional().Nillable(),
		field.Int64("asset_id").Optional().Nillable(),
		field.String("job_type").NotEmpty(),
		field.String("subtitle").Default(""),
		field.Int64("status_id").Optional().Nillable(),
		field.Time("start_time").Optional().Nillable(),
		field.Time("end_time").Optional().Nillable(),
		field.Time("due_date").Optional().Nillable(),
		field.Time("arrival_window_start").Optional().Nillable(),
		field.Time("arrival_window_end").Optional().Nillable(),
		field.String("notes").Default(""),
		field.String("tech_notes").Default(""),
		field.String("billing_type").Default("flat_rate"),
		field.String("visits").Default("[]"),
		field.String("assignments").Default("[]"),
		field.String("custom_fields").Default("[]"),
		field.String("line_items").Default("[]"),
		field.String("subtasks").Default("[]"),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Job) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id"),
		index.Fields("project_id"),
		index.Fields("asset_id"),
		index.Fields("status_id"),
		index.Fields("start_time"),
	}
}
