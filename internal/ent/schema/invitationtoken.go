package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type InvitationToken struct {
	ent.Schema
}

func (InvitationToken) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "invitation_tokens"},
	}
}

func (InvitationToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.String("token_hash").Unique().NotEmpty(),
		field.Int64("user_id"),
		field.Time("expires_at"),
		field.Time("consumed_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
	}
}

func (InvitationToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("token_hash"),
		index.Fields("user_id"),
	}
}
