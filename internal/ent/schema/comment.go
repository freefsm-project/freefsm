package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Comment struct {
	ent.Schema
}

func (Comment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "comments"},
	}
}

func (Comment) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("object_type").NotEmpty(),
		field.Int64("object_id"),
		field.Int64("author_id"),
		field.String("content").NotEmpty(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Comment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "object_id"),
	}
}
