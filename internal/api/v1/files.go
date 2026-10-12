package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

func fileError(w http.ResponseWriter, err error) {
	status, code, message := 500, "file_unavailable", "File storage is unavailable; retry the same operation"
	switch {
	case errors.Is(err, authorization.ErrNotFound) || ent.IsNotFound(err):
		status, code, message = 404, "not_found", "File or original parent unavailable"
	case errors.Is(err, authorization.ErrForbidden):
		status, code, message = 403, "forbidden", "File action not permitted"
	case errors.Is(err, authorization.ErrConflict):
		status, code, message = 409, "file_conflict", "File state changed; review original operation"
	case strings.HasPrefix(err.Error(), "invalid") || strings.HasPrefix(err.Error(), "file extension") || strings.HasPrefix(err.Error(), "file size"):
		status, code, message = 400, "invalid_file", err.Error()
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

func (h *handler) fileLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"max_file_bytes": h.deps.Files.MaxSize(), "max_batch_files": 10, "max_chunk_bytes": services.FileChunkLimit, "digest": "sha256"})
}
func (h *handler) startFileTransfer(w http.ResponseWriter, r *http.Request) {
	var in services.FileTransfer
	if !decodeFileJSON(w, r, &in) {
		return
	}
	out, err := h.deps.Files.StartTransfer(r.Context(), in)
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
	out, err := h.deps.Files.TransferChunk(r.Context(), chi.URLParam(r, "transfer"), offset, data)
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
	disk, err := os.Open(f.FilePath)
	if err != nil {
		fileError(w, err)
		return
	}
	defer disk.Close()
	stat, err := disk.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != f.FileSize {
		fileError(w, fmt.Errorf("durable bytes missing"))
		return
	}
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	w.Header().Set("File-Revision", services.FileRevision(f))
	http.ServeContent(w, r, f.OriginalName, f.CreatedAt, disk)
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
	var in struct {
		Name       string `json:"name"`
		Revision   string `json:"revision"`
		ObjectType string `json:"object_type"`
		ObjectID   int64  `json:"object_id"`
	}
	if !decodeFileJSON(w, r, &in) {
		return
	}
	u := actorFromContext(r.Context())
	original, err := objectref.Parse(in.ObjectType, in.ObjectID)
	if err != nil {
		writeError(w, 400, "original_parent_required", "The original File parent is required; never retarget pending work")
		return
	}
	current, err := h.deps.Files.Metadata(r.Context(), *u.CompanyID, id)
	if err != nil {
		fileError(w, err)
		return
	}
	var expected int64
	if in.Revision == services.FileRevision(current) {
		expected = current.Revision
	}
	err = h.deps.Files.MutateFileAt(r.Context(), *u.CompanyID, id, original, in.Name, remove, expected)
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}
