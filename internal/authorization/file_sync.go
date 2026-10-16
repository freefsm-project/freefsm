package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// FileRevision compares the existing File identity, revision and exact parent.
// Both online commands and coherent sync projections use this one token format.
func FileRevision(f *ent.File) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%s:%d", f.ID, f.CreatedAt.UnixNano(), f.Revision, f.ObjectType, f.ObjectID)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Additions and metadata edits are deltas. Loss of a previously disclosed File
// parent invalidates cached pages as well as new deltas, including an already
// materialized successor. Never replay its old name/capabilities after a move.
func syncFileParentsRetained(previous, current []syncEntry) bool {
	visible := map[string]map[string]string{}
	for _, e := range current {
		visible[e.ID] = e.FileParents
	}
	for _, e := range previous {
		for id, parent := range e.FileParents {
			if visible[e.ID][id] != parent {
				return false
			}
		}
	}
	return true
}

func syncFileParents(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID int64) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT 'job',id FROM jobs WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL
 UNION ALL SELECT 'estimate',id FROM estimates WHERE job_id=$1 AND company_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL
 UNION ALL SELECT 'invoice',id FROM invoices WHERE job_id=$1 AND company_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL ORDER BY 1,2`, jobID, p.CompanyID)
	if err != nil {
		return nil, err
	}
	refs := []objectref.Ref{}
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			rows.Close()
			return nil, err
		}
		ref, err := objectref.Parse(kind, id)
		if err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, ref := range refs {
		if _, err := fileAccess(ctx, tx, p, a, ref, "", 0, false); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				continue
			}
			return nil, err
		}
		_, err := fileAccess(ctx, tx, p, a, ref, "file.upload", a.UserID, false)
		if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		out = append(out, map[string]any{"object_type": ref.ObjectType(), "object_id": strconv.FormatInt(ref.ID, 10), "can_upload": err == nil})
	}
	return out, nil
}

// syncJobFiles reads metadata, never storage paths, digests or transfer bytes.
// Document parent View is checked independently of the containing Job's View.
// Removed rows remain metadata; conversion never silently changes queued intent.
func syncJobFiles(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID int64) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT to_jsonb(f),p.operation_id::text FROM files f
 LEFT JOIN file_publications p ON p.file_id=f.id
 WHERE f.company_id=$1 AND (
 (f.object_type='job' AND f.object_id=$2) OR
 (f.object_type='estimate' AND EXISTS(SELECT 1 FROM estimates e WHERE e.id=f.object_id AND e.job_id=$2 AND e.deleted_at IS NULL AND e.conversion_hidden_at IS NULL)) OR
 (f.object_type='invoice' AND EXISTS(SELECT 1 FROM invoices i WHERE i.id=f.object_id AND i.job_id=$2 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL)))
 ORDER BY f.id`, p.CompanyID, jobID)
	if err != nil {
		return nil, err
	}
	type entry struct {
		file        ent.File
		publication *string
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		var raw []byte
		if err := rows.Scan(&raw, &e.publication); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &e.file); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, e := range entries {
		f := e.file
		ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
		if err != nil {
			return nil, err
		}
		if _, err := fileAccess(ctx, tx, p, a, ref, "", f.UploadedBy, false); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				continue
			}
			return nil, err
		}
		caps := map[string]bool{}
		for _, action := range []string{"download", "rename", "remove"} {
			_, err := fileAccess(ctx, tx, p, a, ref, "file."+action, f.UploadedBy, false)
			if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
				return nil, err
			}
			caps[action] = err == nil && (f.RemovedAt == nil || action == "remove")
		}
		caps["preview"] = caps["download"] && (strings.HasPrefix(f.MimeType, "image/") || f.MimeType == "application/pdf")
		var removedBy any
		if f.RemovedBy != nil {
			removedBy = strconv.FormatInt(*f.RemovedBy, 10)
		}
		out = append(out, map[string]any{
			"id": strconv.FormatInt(f.ID, 10), "object_type": f.ObjectType, "object_id": strconv.FormatInt(f.ObjectID, 10),
			"name": f.OriginalName, "extension": filepath.Ext(f.OriginalName), "size": f.FileSize,
			"mime_type": f.MimeType, "uploaded_by": strconv.FormatInt(f.UploadedBy, 10),
			"publication_id": e.publication, "revision": FileRevision(&f),
			"removed_at": f.RemovedAt, "removed_by": removedBy, "capabilities": caps,
		})
	}
	return out, nil
}
