package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/file"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FileRevision is an opaque comparison token. It changes for edits and parent
// moves, including ABA changes, without exposing an editable counter to clients.
func FileRevision(f *ent.File) string {
	return authorization.FileRevision(f)
}

type FileMutation struct {
	authorization.FileOperation
	Name       string                            `json:"name,omitempty"`
	Revision   string                            `json:"revision"`
	ObjectType string                            `json:"object_type"`
	ObjectID   int64                             `json:"object_id"`
	Batch      *authorization.BatchFileOperation `json:"batch,omitempty"`
}

func (s *FileService) MutateFile(ctx context.Context, company, id int64, in FileMutation, remove bool) error {
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.ErrForbidden
	}
	action := "file.rename"
	if remove {
		action = "file.remove"
	}
	if id, err := uuid.Parse(in.OperationID); err != nil || id == uuid.Nil {
		return authorization.ErrFileOperation
	}
	in.OperationID = uuid.MustParse(in.OperationID).String()
	original, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	in.Name = strings.TrimSpace(in.Name)
	if in.OccurredAt != nil {
		utc := in.OccurredAt.UTC()
		in.OccurredAt = &utc
	}
	return s.mutateFileCommand(ctx, request, company, id, action, in.Name, nil, []objectref.Ref{original}, &in)
}

func (s *FileService) mutateFile(ctx context.Context, request authorization.Request, company, id int64, action, name string, revision []int64, original ...objectref.Ref) error {
	return s.mutateFileCommand(ctx, request, company, id, action, name, revision, original, nil)
}

func (s *FileService) mutateFileCommand(ctx context.Context, request authorization.Request, company, id int64, action, name string, revision []int64, original []objectref.Ref, command *FileMutation) error {
	var f *ent.File
	var ref objectref.Ref
	var err error
	if command != nil {
		ref = original[0]
		f = &ent.File{ObjectType: ref.ObjectType(), ObjectID: ref.ID}
	} else {
		f, err = s.client.File.Query().Where(file.IDEQ(id), file.CompanyIDEQ(company)).Only(ctx)
		if err != nil {
			return err
		}
		ref, err = objectref.Parse(f.ObjectType, f.ObjectID)
		if err != nil {
			return authorization.ErrNotFound
		}
		if err := request.Service.FileAccess(ctx, request.Actor, ref, "", f.UploadedBy); err != nil {
			return err
		}
		if len(original) > 0 && original[0] != ref {
			return authorization.ErrConflict
		}
	}
	var operation []authorization.FileOperation
	var intent string
	if command != nil {
		operation = []authorization.FileOperation{command.FileOperation}
		encoded, err := json.Marshal([]any{action, id, command})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		intent = hex.EncodeToString(digest[:])
		operation[0].Intent = intent
		operation[0].TargetID = id
	}
	return request.Service.FileAction(ctx, request.Actor, ref, action, f.UploadedBy, func(tx pgx.Tx) (int64, any, error) {
		if command != nil {
			if action == "file.remove" && name != "" {
				return 0, nil, authorization.ErrFileOperation
			}
			var published bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_publications WHERE user_id=$1 AND operation_id=$2)`, request.Actor.UserID, command.OperationID).Scan(&published); err != nil {
				return 0, nil, err
			}
			if published {
				return 0, nil, authorization.ErrConflict
			}
			var prior, clientKey string
			err := tx.QueryRow(ctx, `SELECT intent_hash,client_key FROM file_mutation_operations WHERE user_id=$1 AND operation_id=$2`, request.Actor.UserID, command.OperationID).Scan(&prior, &clientKey)
			if err == nil {
				if prior != intent || clientKey != request.Actor.SessionHash {
					return 0, nil, authorization.ErrConflict
				}
				return 0, nil, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, err
			}
		}
		finish := func(effectID int64, detail any) (int64, any, error) {
			if command != nil {
				if _, err := tx.Exec(ctx, `INSERT INTO file_mutation_operations(user_id,operation_id,client_key,intent_hash,file_id) VALUES($1,$2,$3,$4,$5)`, request.Actor.UserID, command.OperationID, request.Actor.SessionHash, intent, id); err != nil {
					return 0, nil, err
				}
			}
			return effectID, detail, nil
		}
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, company).Scan(&encoded); err != nil {
			return 0, nil, err
		}
		current := &ent.File{}
		if err := json.Unmarshal(encoded, current); err != nil {
			return 0, nil, err
		}
		if current.ObjectType != ref.ObjectType() || current.ObjectID != ref.ID || command == nil && current.UploadedBy != f.UploadedBy {
			return 0, nil, authorization.ErrConflict
		}
		if action == "file.remove" && current.RemovedAt != nil {
			return finish(0, nil)
		}
		if current.RemovedAt != nil {
			return 0, nil, authorization.ErrNotFound
		}
		if action == "file.rename" {
			if name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
				return 0, nil, invalidFile("invalid file name")
			}
			if filepath.Ext(name) != filepath.Ext(current.OriginalName) {
				return 0, nil, invalidFile("file extension must be preserved")
			}
			if name == current.OriginalName {
				return finish(0, nil)
			}
		}
		if command != nil {
			revision = []int64{0}
			if command.Revision == FileRevision(current) {
				revision[0] = current.Revision
			}
		}
		if len(revision) != 1 || revision[0] != current.Revision {
			return 0, nil, authorization.ErrConflict
		}
		if action == "file.remove" {
			_, err = tx.Exec(ctx, `UPDATE files SET removed_at=clock_timestamp(),removed_by=$2,revision=revision+1 WHERE id=$1`, id, request.Actor.UserID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE files SET original_name=$2,revision=revision+1 WHERE id=$1`, id, name)
		}
		if err != nil {
			return 0, nil, err
		}
		return finish(id, map[string]any{"old_name": current.OriginalName, "name": name, "revision": current.Revision + 1, "parent_type": ref.ObjectType(), "parent_id": ref.ID})
	}, operation...)
}
