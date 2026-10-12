package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/file"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// FileRevision is an opaque comparison token. It changes for edits and parent
// moves, including ABA changes, without exposing an editable counter to clients.
func FileRevision(f *ent.File) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%s:%d", f.ID, f.CreatedAt.UnixNano(), f.Revision, f.ObjectType, f.ObjectID)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *FileService) MutateFileAt(ctx context.Context, company, id int64, original objectref.Ref, name string, remove bool, revision int64) error {
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.ErrForbidden
	}
	action := "file.rename"
	if remove {
		action = "file.remove"
	}
	return s.mutateFile(ctx, request, company, id, action, strings.TrimSpace(name), []int64{revision}, original)
}

func (s *FileService) mutateFile(ctx context.Context, request authorization.Request, company, id int64, action, name string, revision []int64, original ...objectref.Ref) error {
	f, err := s.client.File.Query().Where(file.IDEQ(id), file.CompanyIDEQ(company)).Only(ctx)
	if err != nil {
		return err
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil {
		return authorization.ErrNotFound
	}
	if len(original) > 0 && original[0] != ref {
		return authorization.ErrConflict
	}
	return request.Service.FileAction(ctx, request.Actor, ref, action, f.UploadedBy, func(tx pgx.Tx) (int64, any, error) {
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, company).Scan(&encoded); err != nil {
			return 0, nil, err
		}
		current := &ent.File{}
		if err := json.Unmarshal(encoded, current); err != nil {
			return 0, nil, err
		}
		if current.ObjectType != ref.ObjectType() || current.ObjectID != ref.ID || current.UploadedBy != f.UploadedBy {
			return 0, nil, authorization.ErrConflict
		}
		if action == "file.remove" && current.RemovedAt != nil {
			return 0, nil, nil
		}
		if current.RemovedAt != nil {
			return 0, nil, authorization.ErrNotFound
		}
		if action == "file.rename" {
			if name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
				return 0, nil, fmt.Errorf("invalid file name")
			}
			if filepath.Ext(name) != filepath.Ext(current.OriginalName) {
				return 0, nil, fmt.Errorf("file extension must be preserved")
			}
			if name == current.OriginalName {
				return 0, nil, nil
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
		return id, map[string]any{"old_name": current.OriginalName, "name": name, "revision": current.Revision + 1, "parent_type": ref.ObjectType(), "parent_id": ref.ID}, err
	})
}
