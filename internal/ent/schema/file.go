package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type File struct {
	ent.Schema
}

func (File) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "files"},
	}
}

func (File) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("company_id"),
		field.String("object_type").NotEmpty(),
		field.Int64("object_id"),
		field.String("original_name").NotEmpty(),
		field.String("stored_name").NotEmpty(),
		field.String("mime_type").NotEmpty(),
		field.Int64("file_size"),
		field.String("file_path").NotEmpty(),
		field.Int64("uploaded_by"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (File) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("object_type", "object_id"),
		index.Fields("company_id"),
	}
}
