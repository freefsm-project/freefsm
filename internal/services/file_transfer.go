package services

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const FileChunkLimit = 1 << 20

type FileTransfer struct {
	ID            string     `json:"id"`
	PublicationID string     `json:"publication_id"`
	ObjectType    string     `json:"object_type"`
	ObjectID      int64      `json:"object_id"`
	Name          string     `json:"name"`
	Size          int64      `json:"size"`
	Digest        string     `json:"sha256"`
	Transferred   int64      `json:"transferred"`
	OccurredAt    *time.Time `json:"occurred_at,omitempty"`
}

func (s *FileService) StartTransfer(ctx context.Context, in FileTransfer) (FileTransfer, error) {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return in, authorization.ErrForbidden
	}
	if id, err := uuid.Parse(in.ID); err != nil || id == uuid.Nil {
		return in, fmt.Errorf("invalid transfer ID")
	}
	if err := (authorization.FileOperation{OperationID: in.PublicationID, OccurredAt: in.OccurredAt}).Validate(); err != nil {
		return in, err
	}
	if in.OccurredAt != nil {
		occurred := in.OccurredAt.UTC().Truncate(time.Microsecond)
		in.OccurredAt = &occurred
	}
	if d, err := hex.DecodeString(in.Digest); err != nil || len(d) != 32 {
		return in, fmt.Errorf("invalid SHA-256")
	}
	if in.Size <= 0 || in.Size > s.maxSize || in.Transferred != 0 {
		return in, fmt.Errorf("invalid transfer size")
	}
	ref, err := objectref.Parse(in.ObjectType, in.ObjectID)
	if err != nil {
		return in, err
	}
	err = r.Service.FileAction(ctx, r.Actor, ref, "file.upload", r.Actor.UserID, func(tx pgx.Tx) (int64, any, error) {
		_, err := tx.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO NOTHING`, in.ID, r.Actor.UserID, r.Actor.SessionHash, in.PublicationID, in.ObjectType, in.ObjectID, in.Name, in.Size, in.Digest, in.OccurredAt)
		if err != nil {
			return 0, nil, err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT publication_id=$4 AND object_type=$5 AND object_id=$6 AND name=$7 AND size=$8 AND sha256=$9 AND occurred_at IS NOT DISTINCT FROM $10::timestamptz FROM file_transfers WHERE id=$1 AND user_id=$2 AND session_hash=$3`, in.ID, r.Actor.UserID, r.Actor.SessionHash, in.PublicationID, in.ObjectType, in.ObjectID, in.Name, in.Size, in.Digest, in.OccurredAt).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, authorization.ErrNotFound
		}
		if err != nil {
			return 0, nil, err
		}
		if !same {
			return 0, nil, authorization.ErrConflict
		}
		return 0, nil, nil
	})
	if err != nil {
		return in, err
	}
	return s.Transfer(ctx, in.ID)
}

func (s *FileService) Transfer(ctx context.Context, id string) (FileTransfer, error) {
	var out FileTransfer
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return out, authorization.ErrForbidden
	}
	if _, err := uuid.Parse(id); err != nil {
		return out, authorization.ErrNotFound
	}
	err := r.Service.Pool.QueryRow(ctx, `SELECT id::text,publication_id::text,object_type,object_id,name,size,sha256,transferred,occurred_at FROM file_transfers WHERE id=$1 AND user_id=$2 AND session_hash=$3`, id, r.Actor.UserID, r.Actor.SessionHash).Scan(&out.ID, &out.PublicationID, &out.ObjectType, &out.ObjectID, &out.Name, &out.Size, &out.Digest, &out.Transferred, &out.OccurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, authorization.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	ref, err := objectref.Parse(out.ObjectType, out.ObjectID)
	if err != nil {
		return out, err
	}
	return out, r.Service.FileAccess(ctx, r.Actor, ref, "file.upload", r.Actor.UserID)
}

func (s *FileService) TransferChunk(ctx context.Context, id string, offset int64, data []byte) (FileTransfer, error) {
	in, err := s.Transfer(ctx, id)
	if err != nil {
		return in, err
	}
	if len(data) == 0 || len(data) > FileChunkLimit || offset < 0 || offset > in.Size-int64(len(data)) {
		return in, fmt.Errorf("invalid chunk bounds")
	}
	r, _ := authorization.FromContext(ctx)
	ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	err = r.Service.FileAction(ctx, r.Actor, ref, "file.upload", r.Actor.UserID, func(tx pgx.Tx) (int64, any, error) {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT transferred FROM file_transfers WHERE id=$1 FOR UPDATE`, id).Scan(&current); err != nil {
			return 0, nil, err
		}
		if offset < current {
			var prior []byte
			err := tx.QueryRow(ctx, `SELECT data FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=$2`, id, offset).Scan(&prior)
			if err != nil || !bytes.Equal(prior, data) {
				return 0, nil, authorization.ErrConflict
			}
			return 0, nil, nil
		}
		if offset != current {
			return 0, nil, authorization.ErrConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO file_transfer_chunks VALUES($1,$2,$3)`, id, offset, data); err != nil {
			return 0, nil, err
		}
		_, err := tx.Exec(ctx, `UPDATE file_transfers SET transferred=transferred+$2 WHERE id=$1`, id, len(data))
		return 0, nil, err
	})
	if err != nil {
		return in, err
	}
	return s.Transfer(ctx, id)
}

func (s *FileService) PublishTransfer(ctx context.Context, id string) (*ent.File, error) {
	in, err := s.Transfer(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Transferred != in.Size {
		return nil, authorization.ErrConflict
	}
	r, _ := authorization.FromContext(ctx)
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil {
		return nil, err
	}
	ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	reader := &transferReader{ctx: ctx, request: r, id: id, size: in.Size}
	return s.Create(ctx, p.CompanyID, ref, in.Name, "", in.Size, reader, r.Actor.UserID, FilePublication{ID: in.PublicationID, Digest: in.Digest, OccurredAt: in.OccurredAt})
}

// Read one immutable bounded chunk at a time, independent of configured total
// upload size. No filesystem extraction or whole-transfer buffering is needed.
type transferReader struct {
	ctx          context.Context
	request      authorization.Request
	id           string
	size, offset int64
	chunk        *bytes.Reader
}

func (r *transferReader) Read(p []byte) (int, error) {
	if r.chunk == nil || r.chunk.Len() == 0 {
		if r.offset == r.size {
			return 0, io.EOF
		}
		var data []byte
		if err := r.request.Service.Pool.QueryRow(r.ctx, `SELECT data FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=$2`, r.id, r.offset).Scan(&data); err != nil {
			return 0, err
		}
		r.chunk = bytes.NewReader(data)
	}
	n, err := r.chunk.Read(p)
	r.offset += int64(n)
	return n, err
}
