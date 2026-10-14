package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func fileError(w http.ResponseWriter, err error) {
	status, code, message := 500, "file_unavailable", "File storage is unavailable; retry the same operation"
	var validation *authorization.FileValidationError
	switch {
	case errors.Is(err, authorization.ErrFileOperation):
		status, code, message = 400, "invalid_operation", "A nonzero operation UUID and a valid nonfuture occurrence time are required"
	case errors.Is(err, authorization.ErrNotFound) || ent.IsNotFound(err):
		status, code, message = 404, "not_found", "File or original parent unavailable"
	case errors.Is(err, authorization.ErrForbidden):
		status, code, message = 403, "forbidden", "File action not permitted"
	case errors.Is(err, authorization.ErrConflict):
		status, code, message = 409, "file_conflict", "File state changed; review original operation"
	case errors.As(err, &validation):
		status, code, message = 400, "invalid_file", validation.Error()
	}
	writeError(w, status, code, message)
}

func decodeFileJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		writeError(w, 400, "invalid_json", "Invalid file request")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_json", "Only one JSON value is allowed")
		return false
	}
	return true
}

// DisallowUnknownFields still matches struct fields case-insensitively. Check
// the exact request properties before typed decoding to enforce the published
// additionalProperties:false contract, including canonical-plus-variant input.
func decodeExactJSON(w http.ResponseWriter, r *http.Request, out any, properties ...string) bool {
	var raw json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		writeError(w, 400, "invalid_json", "Invalid file request")
		return false
	}
	for name := range fields {
		if !slices.Contains(properties, name) {
			writeError(w, 400, "invalid_json", "Invalid file request")
			return false
		}
		if !slices.Contains([]string{"field_notes", "completed", "status_id", "values"}, name) && bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			writeError(w, 400, "invalid_json", "A supplied property must have its declared type")
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			writeError(w, 400, "invalid_json", "Invalid object")
			return false
		}
		name := key.(string)
		if seen[name] {
			writeError(w, 400, "invalid_json", "Duplicate property")
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			writeError(w, 400, "invalid_json", "Invalid property")
			return false
		}
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(out); err != nil {
		writeError(w, 400, "invalid_json", "Invalid file request")
		return false
	}
	return true
}

func (h *handler) fileLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"max_file_bytes": h.deps.Files.MaxSize(), "max_batch_files": 10, "max_chunk_bytes": services.FileChunkLimit, "digest": "sha256"})
}
func (h *handler) startFileTransfer(w http.ResponseWriter, r *http.Request) {
	var request fileTransferRequest
	if !decodeExactJSON(w, r, &request, "id", "publication_id", "object_type", "object_id", "name", "size", "sha256", "transferred", "verified", "occurred_at") {
		return
	}
	in, err := request.transfer()
	if err != nil {
		writeError(w, 400, "invalid_file", "Invalid transfer request")
		return
	}
	out, err := h.deps.Files.StartTransfer(r.Context(), in, authorization.FileOperation{OperationID: r.Header.Get("Operation-ID")})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *handler) getFileTransfer(w http.ResponseWriter, r *http.Request) {
	out, err := h.deps.Files.Transfer(r.Context(), chi.URLParam(r, "transfer"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *handler) verifyFileTransfer(w http.ResponseWriter, r *http.Request) {
	out, err := h.deps.Files.VerifyTransfer(r.Context(), chi.URLParam(r, "transfer"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *handler) lookupFileTransfer(w http.ResponseWriter, r *http.Request) {
	var in services.TransferLookup
	if !decodeExactJSON(w, r, &in, "object_type", "object_id", "sha256") {
		return
	}
	if !validTransferParent(in.ObjectType, in.ObjectID) {
		writeError(w, 400, "invalid_file", "Invalid transfer parent")
		return
	}
	out, err := h.deps.Files.LookupTransfer(r.Context(), in)
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *handler) putFileChunk(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid_offset", "An exact byte offset is required")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, services.FileChunkLimit))
	if err != nil {
		writeError(w, 413, "chunk_too_large", "Chunk limit exceeded")
		return
	}
	out, err := h.deps.Files.TransferChunk(r.Context(), chi.URLParam(r, "transfer"), offset, data, authorization.FileOperation{OperationID: r.Header.Get("Operation-ID")})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *handler) publishFileTransfer(w http.ResponseWriter, r *http.Request) {
	out, err := h.deps.Files.PublishTransfer(r.Context(), chi.URLParam(r, "transfer"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": out.ID, "revision": services.FileRevision(out)})
}

func (h *handler) fileTransportReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		fileError(w, authorization.ErrForbidden)
		return
	}
	out, err := req.Service.FileTransportReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		fileError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (h *handler) fileOperationReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		fileError(w, authorization.ErrForbidden)
		return
	}
	out, err := req.Service.FileReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		fileError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		fileError(w, authorization.ErrNotFound)
		return
	}
	u := actorFromContext(r.Context())
	f, err := h.deps.Files.GetByID(r.Context(), *u.CompanyID, id)
	if err != nil {
		fileError(w, err)
		return
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil {
		fileError(w, authorization.ErrNotFound)
		return
	}
	a, _ := authorization.FromContext(r.Context())
	if err := a.Service.FileAccess(r.Context(), a.Actor, ref, "file.download", f.UploadedBy); err != nil {
		fileError(w, err)
		return
	}
	// Serve a restricted, verified snapshot, not a mutable disk handle whose
	// bytes could change between hashing and range delivery. No historical
	// digest is invented for installed Files whose digest was never recorded.
	var disk *os.File
	var digest []byte
	err = a.Service.FileAction(r.Context(), a.Actor, ref, "file.download", f.UploadedBy, func(tx pgx.Tx) (int64, any, error) {
		var current bool
		if err := tx.QueryRow(r.Context(), `SELECT removed_at IS NULL AND object_type=$2 AND object_id=$3 AND revision=$4 FROM files WHERE id=$1 FOR SHARE`, f.ID, f.ObjectType, f.ObjectID, f.Revision).Scan(&current); err != nil {
			return 0, nil, err
		}
		if !current {
			return 0, nil, authorization.ErrNotFound
		}
		source, err := os.Open(f.FilePath)
		if err != nil {
			return 0, nil, err
		}
		defer source.Close()
		stat, err := source.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != f.FileSize {
			return 0, nil, fmt.Errorf("durable bytes missing")
		}
		// Downloads remain readable during backup; never add/remove transient
		// snapshots inside the concurrently inventoried durable upload tree.
		disk, err = os.CreateTemp("", "freefsm-download-*")
		if err != nil {
			return 0, nil, err
		}
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(disk, hash), io.LimitReader(source, f.FileSize+1))
		if err != nil {
			return 0, nil, err
		}
		digest = hash.Sum(nil)
		if n != f.FileSize || f.Sha256 != nil && *f.Sha256 != hex.EncodeToString(digest) {
			return 0, nil, fmt.Errorf("durable bytes mismatch")
		}
		_, err = disk.Seek(0, io.SeekStart)
		return 0, nil, err
	})
	if disk != nil {
		defer os.Remove(disk.Name())
		defer disk.Close()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	w.Header().Set("ETag", `"sha256-`+hex.EncodeToString(digest)+`"`)
	w.Header().Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest)+":")
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	w.Header().Set("File-Revision", services.FileRevision(f))
	http.ServeContent(&fileContentResponse{ResponseWriter: w}, r, f.OriginalName, f.CreatedAt, disk)
}

// Keep standard conditional/range behavior while retaining the API's typed
// error envelope. ServeContent's plain-text error must not escape this boundary.
type fileContentResponse struct {
	http.ResponseWriter
	failed bool
}

func (w *fileContentResponse) WriteHeader(status int) {
	if status >= 400 {
		w.failed = true
		w.Header().Del("Content-Length")
		code, message := "file_unavailable", "File response unavailable"
		if status == 416 {
			code, message = "invalid_range", "Requested byte range is not satisfiable"
		}
		writeError(w.ResponseWriter, status, code, message)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *fileContentResponse) Write(p []byte) (int, error) {
	if w.failed {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}
func (h *handler) renameFile(w http.ResponseWriter, r *http.Request) { h.mutateFile(w, r, false) }
func (h *handler) removeFile(w http.ResponseWriter, r *http.Request) { h.mutateFile(w, r, true) }
func (h *handler) fileMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		fileError(w, authorization.ErrNotFound)
		return
	}
	u := actorFromContext(r.Context())
	f, err := h.deps.Files.Metadata(r.Context(), *u.CompanyID, id)
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": f.ID, "name": f.OriginalName, "size": f.FileSize, "mime_type": f.MimeType, "object_type": f.ObjectType, "object_id": f.ObjectID, "uploaded_by": f.UploadedBy, "removed_at": f.RemovedAt, "removed_by": f.RemovedBy, "revision": services.FileRevision(f)})
}
func (h *handler) mutateFile(w http.ResponseWriter, r *http.Request, remove bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		fileError(w, authorization.ErrNotFound)
		return
	}
	var in services.FileMutation
	if !decodeExactJSON(w, r, &in, "operation_id", "occurred_at", "name", "revision", "object_type", "object_id") {
		return
	}
	u := actorFromContext(r.Context())
	err = h.deps.Files.MutateFile(r.Context(), *u.CompanyID, id, in, remove)
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}
