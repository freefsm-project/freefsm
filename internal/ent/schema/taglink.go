package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TagLink struct {
	ent.Schema
}

func (TagLink) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tag_links"},
	}
}

func (TagLink) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id").Optional().Nillable(),
		field.Int64("tag_id"),
		field.String("object_type").NotEmpty(),
		field.Int64("object_id"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (TagLink) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "object_id"),
		index.Fields("tag_id", "object_type", "object_id").Unique(),
	}
}
