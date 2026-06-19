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
		field.String("email").Unique().NotEmpty(),
		field.String("password_hash").NotEmpty(),
		field.String("name").NotEmpty(),
		field.String("role").Default("tech"),
		field.Bool("is_active").Default(true),
		field.Bool("force_password_change").Default(false),
		field.Time("welcome_email_sent_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
