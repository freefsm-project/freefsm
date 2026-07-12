package schema

import (
	"fmt"
	"strings"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Status struct {
	ent.Schema
}

func (Status) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "statuses"},
	}
}

func (Status) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("workflow_id"),
		field.String("name").NotEmpty().Validate(func(v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("status name must not be blank")
			}
			return nil
		}),
		field.String("color").Default("#6B7280"),
		field.Int("sort_order").Default(0),
		field.String("category_key").NotEmpty().Validate(func(v string) error {
			for _, key := range []string{"job:new", "job:travel_time", "job:in_progress", "job:pending", "job:completed", "job:canceled", "project:new", "project:in_progress", "project:pending", "project:completed", "project:canceled", "estimate:draft", "estimate:estimate", "estimate:sent", "estimate:accepted", "estimate:rejected", "estimate:completed", "invoice:draft", "invoice:invoiced", "invoice:sent", "invoice:partially_paid", "invoice:paid", "invoice:void"} {
				if v == key {
					return nil
				}
			}
			return fmt.Errorf("invalid status category %q", v)
		}),
		field.Int("category_order").Default(1).Positive(),
		field.Bool("is_category_default").Default(false),
		field.Time("created_at").Default(time.Now),
	}
}

func (Status) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("workflow", StatusWorkflow.Type).
			Ref("statuses").
			Field("workflow_id").
			Unique().
			Required(),
	}
}

func (Status) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("workflow_id"),
		index.Fields("workflow_id", "category_key", "category_order").Unique(),
	}
}
