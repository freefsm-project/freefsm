package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type User struct {
	ent.Schema
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "users"},
	}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("email").Unique().NotEmpty(),
		field.String("password_hash").NotEmpty(),
		field.String("name").NotEmpty(),
		field.String("role").Default("tech"),
		field.String("font_size").Default("medium"),
		field.String("last_schedule_tab").Default("calendar"),
		field.String("last_schedule_period").Default("month"),
		field.Bool("is_active").Default(true),
		field.Bool("force_password_change").Default(false),
		field.Time("welcome_email_sent_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("onboarding_completed_at").Optional().Nillable(),
	}
}
