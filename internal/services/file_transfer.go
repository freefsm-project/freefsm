package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const FileChunkLimit = 1 << 20

type TransferLookup struct {
	ObjectType string `json:"object_type"`
	ObjectID   int64  `json:"object_id"`
	Digest     string `json:"sha256"`
}

// LookupTransfer is deliberately not a content-addressed global store. A digest
// can only find this paired caller's complete transfer at this original parent.
// The returned original identity is reused, never cloned into another parent.
func (s *FileService) LookupTransfer(ctx context.Context, in TransferLookup) (FileTransfer, error) {
	var out FileTransfer
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return out, authorization.ErrForbidden
	}
	ref, err := objectref.Parse(in.ObjectType, in.ObjectID)
	if err != nil {
		return out, authorization.ErrNotFound
	}
	if err := r.Service.FileAccess(ctx, r.Actor, ref, "file.upload", r.Actor.UserID); err != nil {
		return out, err
	}
	if d, err := hex.DecodeString(in.Digest); err != nil || len(d) != 32 || in.Digest != strings.ToLower(in.Digest) {
		return out, invalidFile("invalid SHA-256")
	}
	var id string
	err = r.Service.Pool.QueryRow(ctx, `SELECT id::text FROM file_transfers WHERE user_id=$1 AND session_hash=$2 AND object_type=$3 AND object_id=$4 AND sha256=$5 AND transferred=size ORDER BY created_at DESC,id LIMIT 1`, r.Actor.UserID, r.Actor.SessionHash, in.ObjectType, in.ObjectID, in.Digest).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, authorization.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	return s.VerifyTransfer(ctx, id)
}

type FileTransfer struct {
	ID            string     `json:"id"`
	PublicationID string     `json:"publication_id"`
	ObjectType    string     `json:"object_type"`
	ObjectID      int64      `json:"object_id"`
	Name          string     `json:"name"`
	Size          int64      `json:"size"`
	Digest        string     `json:"sha256"`
	Transferred   int64      `json:"transferred"`
	Verified      bool       `json:"verified"`
	OccurredAt    *time.Time `json:"occurred_at,omitempty"`
}

func (s *FileService) StartTransfer(ctx context.Context, in FileTransfer, operation ...authorization.FileOperation) (FileTransfer, error) {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return in, authorization.ErrForbidden
	}
	if len(operation) != 1 {
		return in, authorization.ErrFileOperation
	}
	validate := func() error {
		if in.ObjectID <= 0 || (in.ObjectType != "job" && in.ObjectType != "estimate" && in.ObjectType != "invoice") {
			return invalidFile("invalid transfer parent")
		}
		if id, err := uuid.Parse(in.ID); err != nil || id == uuid.Nil {
			return invalidFile("invalid transfer ID")
		}
		if err := (authorization.FileOperation{OperationID: in.PublicationID, OccurredAt: in.OccurredAt}).Validate(); err != nil {
			return err
		}
		if d, err := hex.DecodeString(in.Digest); err != nil || len(d) != 32 || in.Digest != strings.ToLower(in.Digest) {
			return invalidFile("invalid SHA-256")
		}
		if in.Size <= 0 || in.Size > s.maxSize || in.Transferred != 0 || in.Verified {
			return invalidFile("invalid transfer size")
		}
		if strings.TrimSpace(in.Name) == "" {
			return invalidFile("invalid file name")
		}
		if _, err := objectref.Parse(in.ObjectType, in.ObjectID); err != nil {
			return invalidFile("invalid transfer parent")
		}
		return nil
	}
	ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	command := authorization.FileTransportCommand{FileOperation: operation[0], TransferID: in.ID, Kind: "start", Intent: in, Validate: validate}
	err := r.Service.FileTransferAction(ctx, r.Actor, ref, command, func(tx pgx.Tx) (bool, map[string]any, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(user_id,session_hash,id) DO NOTHING`, in.ID, r.Actor.UserID, r.Actor.SessionHash, in.PublicationID, in.ObjectType, in.ObjectID, in.Name, in.Size, in.Digest, in.OccurredAt)
		if err != nil {
			return false, nil, err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT publication_id=$4 AND object_type=$5 AND object_id=$6 AND name=$7 AND size=$8 AND sha256=$9 AND occurred_at IS NOT DISTINCT FROM $10::timestamptz FROM file_transfers WHERE id=$1 AND user_id=$2 AND session_hash=$3`, in.ID, r.Actor.UserID, r.Actor.SessionHash, in.PublicationID, in.ObjectType, in.ObjectID, in.Name, in.Size, in.Digest, in.OccurredAt).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil, authorization.ErrNotFound
		}
		if err != nil {
			return false, nil, err
		}
		if !same {
			return false, nil, authorization.ErrConflict
		}
		return tag.RowsAffected() == 1, map[string]any{"stage": "transfer_started", "size": in.Size}, nil
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

func (s *FileService) TransferChunk(ctx context.Context, id string, offset int64, data []byte, operation ...authorization.FileOperation) (FileTransfer, error) {
	if len(operation) != 1 {
		return FileTransfer{}, authorization.ErrFileOperation
	}
	in, err := s.Transfer(ctx, id)
	transferErr := err
	r, _ := authorization.FromContext(ctx)
	ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	digest := sha256.Sum256(data)
	command := authorization.FileTransportCommand{FileOperation: operation[0], TransferID: id, Kind: "chunk", Intent: map[string]any{"transfer_id": id, "offset": offset, "sha256": hex.EncodeToString(digest[:]), "size": len(data)}, Validate: func() error {
		if transferErr != nil {
			return transferErr
		}
		if in.Size > s.maxSize || len(data) == 0 || len(data) > FileChunkLimit || offset < 0 || offset > in.Size-int64(len(data)) {
			return invalidFile("invalid chunk bounds")
		}
		return nil
	}}
	err = r.Service.FileTransferAction(ctx, r.Actor, ref, command, func(tx pgx.Tx) (bool, map[string]any, error) {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT transferred FROM file_transfers WHERE id=$1 AND user_id=$2 AND session_hash=$3 FOR UPDATE`, id, r.Actor.UserID, r.Actor.SessionHash).Scan(&current); err != nil {
			return false, nil, err
		}
		if offset < current {
			var prior []byte
			err := tx.QueryRow(ctx, `SELECT data FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=$2 AND user_id=$3 AND session_hash=$4`, id, offset, r.Actor.UserID, r.Actor.SessionHash).Scan(&prior)
			if err != nil || !bytes.Equal(prior, data) {
				return false, nil, authorization.ErrConflict
			}
			return false, nil, nil
		}
		if offset != current {
			return false, nil, authorization.ErrConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data,user_id,session_hash) VALUES($1,$2,$3,$4,$5)`, id, offset, data, r.Actor.UserID, r.Actor.SessionHash); err != nil {
			return false, nil, err
		}
		_, err := tx.Exec(ctx, `UPDATE file_transfers SET transferred=transferred+$2 WHERE id=$1 AND user_id=$3 AND session_hash=$4`, id, len(data), r.Actor.UserID, r.Actor.SessionHash)
		return true, map[string]any{"stage": "transfer_chunk", "offset": offset, "size": len(data)}, err
	})
	if err != nil {
		return in, err
	}
	return s.Transfer(ctx, id)
}

// VerifyTransfer validates temporary bytes independently of publication. The
// result is a fresh observation, never a cached assertion about durable storage.
// No File, attachment or collaboration event is created here.
func (s *FileService) VerifyTransfer(ctx context.Context, id string) (FileTransfer, error) {
	in, err := s.Transfer(ctx, id)
	if err != nil {
		return in, err
	}
	if in.Transferred != in.Size {
		return in, authorization.ErrConflict
	}
	if in.Size > s.maxSize {
		return in, invalidFile("invalid transfer size: current limit exceeded")
	}
	r, _ := authorization.FromContext(ctx)
	ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
	err = r.Service.FileAction(ctx, r.Actor, ref, "file.upload", r.Actor.UserID, func(tx pgx.Tx) (int64, any, error) {
		var size int64
		if err := tx.QueryRow(ctx, `SELECT transferred FROM file_transfers WHERE id=$1 AND user_id=$2 AND session_hash=$3 FOR UPDATE`, id, r.Actor.UserID, r.Actor.SessionHash).Scan(&size); err != nil {
			return 0, nil, err
		}
		if size != in.Size {
			return 0, nil, authorization.ErrConflict
		}
		var durableSize int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(octet_length(data)),0) FROM file_transfer_chunks WHERE transfer_id=$1 AND user_id=$2 AND session_hash=$3`, id, r.Actor.UserID, r.Actor.SessionHash).Scan(&durableSize); err != nil {
			return 0, nil, err
		}
		if durableSize != in.Size {
			return 0, nil, fmt.Errorf("inconsistent durable transfer bytes")
		}
		// Validation scratch is disposable, not part of the durable File
		// inventory. In particular, a process interruption must not leave a
		// phantom attachment payload for backup to inventory.
		f, err := os.CreateTemp("", "freefsm-transfer-verify-*")
		if err != nil {
			return 0, nil, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		digest := sha256.New()
		reader := &transferReader{ctx: ctx, query: tx, id: id, size: in.Size, userID: r.Actor.UserID, sessionHash: r.Actor.SessionHash}
		n, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(reader, in.Size+1))
		if err != nil {
			return 0, nil, err
		}
		if n != in.Size || hex.EncodeToString(digest.Sum(nil)) != in.Digest {
			return 0, nil, invalidFile("invalid file content: digest mismatch")
		}
		if _, err := s.validateContent(f.Name(), in.Name); err != nil {
			return 0, nil, err
		}
		in.Verified = true
		return 0, nil, nil
	})
	return in, err
}

func (s *FileService) PublishTransfer(ctx context.Context, id string) (*ent.File, error) {
	in, err := s.VerifyTransfer(ctx, id)
	if err != nil {
		if r, ok := authorization.FromContext(ctx); ok && r.Actor.Channel == "api" && in.PublicationID != "" {
			ref, _ := objectref.Parse(in.ObjectType, in.ObjectID)
			err = r.Service.RejectFilePublication(ctx, r.Actor, in.PublicationID, filePublicationIntent(ref, in.Name, in.Size, in.Digest, in.OccurredAt), err)
		}
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
	reader := &transferReader{ctx: ctx, query: r.Service.Pool, id: id, size: in.Size, userID: r.Actor.UserID, sessionHash: r.Actor.SessionHash}
	return s.Create(ctx, p.CompanyID, ref, in.Name, "", in.Size, reader, r.Actor.UserID, FilePublication{ID: in.PublicationID, Digest: in.Digest, OccurredAt: in.OccurredAt})
}

// Read one immutable bounded chunk at a time, independent of configured total
// upload size. No filesystem extraction or whole-transfer buffering is needed.
type transferReader struct {
	ctx   context.Context
	query interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}
	id           string
	userID       int64
	sessionHash  string
	size, offset int64
	chunk        *bytes.Reader
}

func (r *transferReader) Read(p []byte) (int, error) {
	if r.chunk == nil || r.chunk.Len() == 0 {
		if r.offset == r.size {
			return 0, io.EOF
		}
		var data []byte
		if err := r.query.QueryRow(r.ctx, `SELECT data FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=$2 AND user_id=$3 AND session_hash=$4`, r.id, r.offset, r.userID, r.sessionHash).Scan(&data); err != nil {
			return 0, err
		}
		if len(data) == 0 || int64(len(data)) > r.size-r.offset {
			return 0, fmt.Errorf("inconsistent durable transfer bytes")
		}
		r.chunk = bytes.NewReader(data)
	}
	n, err := r.chunk.Read(p)
	r.offset += int64(n)
	return n, err
}
