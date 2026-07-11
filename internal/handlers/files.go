package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

type FileHandler struct {
	svc         *services.FileService
	activitySvc *services.ActivityService
	policySvc   *services.PolicyService
	objects     objectref.Directory
}

func NewFileHandler(svc *services.FileService, activitySvc *services.ActivityService, policySvc *services.PolicyService, objects objectref.Directory) *FileHandler {
	return &FileHandler{svc: svc, activitySvc: activitySvc, policySvc: policySvc, objects: objects}
}

func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.svc.MaxSize()+1024*1024)
	if err := r.ParseMultipartForm(h.svc.MaxSize()); err != nil {
		http.Error(w, "File too large", 413)
		return
	}
	defer r.MultipartForm.RemoveAll()

	fileHeader := r.MultipartForm.File["file"]
	if len(fileHeader) == 0 {
		http.Error(w, "No file uploaded", 400)
		return
	}

	fh := fileHeader[0]
	objectType, ok := multipartValue(r, "object_type")
	if !ok {
		http.Error(w, "Invalid object type", 400)
		return
	}
	objectIDStr, ok := multipartValue(r, "object_id")
	if !ok {
		http.Error(w, "Invalid object ID", 400)
		return
	}
	objectID, err := strconv.ParseInt(objectIDStr, 10, 64)
	if err != nil || objectID <= 0 {
		http.Error(w, "Invalid object ID", 400)
		return
	}
	ref, err := objectref.Parse(objectType, objectID)
	if err != nil || !h.svc.SupportsFiles(ref) {
		http.Error(w, "Invalid object type", 400)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile) {
		http.Error(w, "Forbidden", 403)
		return
	}

	f, err := fh.Open()
	if err != nil {
		http.Error(w, "Cannot open file", 500)
		return
	}
	defer f.Close()

	mimeType, err := detectUploadedFileMime(f)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	_, err = h.svc.Create(r.Context(), ref, fh.Filename, mimeType, fh.Size, f, u.ID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid MIME type:") {
			http.Error(w, "Invalid file type", 400)
			return
		}
		if strings.HasPrefix(err.Error(), "file size ") {
			http.Error(w, "File too large", 413)
			return
		}
		if strings.HasPrefix(err.Error(), "target ") || strings.HasPrefix(err.Error(), "invalid object type:") {
			http.Error(w, "Invalid attachment target", 400)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}

	entityName := objectDisplayName(r.Context(), h.objects, ref)
	h.activitySvc.Record(r.Context(), u.ID, "file_uploaded", ref, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   fh.Filename,
	})

	redirect, _ := multipartValue(r, "redirect")
	redirect = safeLocalRedirect(redirect)
	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(200)
}

func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExistsAny(r.Context(), ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyRead) {
		http.Error(w, "Forbidden", 403)
		return
	}

	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	} else if services.IsInlineMimeType(f.MimeType) {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.OriginalName))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.FileSize, 10))
	http.ServeFile(w, r, f.FilePath)
}

func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid file ID", 400)
		return
	}

	f, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExists(r.Context(), ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyDelete) && !(f.UploadedBy == u.ID && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile)) {
		http.Error(w, "Forbidden", 403)
		return
	}

	redirect := safeLocalRedirect(r.FormValue("redirect"))

	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	entityName := objectDisplayName(r.Context(), h.objects, ref)
	h.activitySvc.Record(r.Context(), u.ID, "file_deleted", ref, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   f.OriginalName,
	})

	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(200)
}

func (h *FileHandler) Rename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid file ID", 400)
		return
	}

	f, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExists(r.Context(), ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyDelete) && !(f.UploadedBy == u.ID && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile)) {
		http.Error(w, "Forbidden", 403)
		return
	}

	redirect := safeLocalRedirect(r.FormValue("redirect"))
	name := strings.TrimSpace(r.FormValue("original_name"))
	if err := h.svc.Rename(r.Context(), id, name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	entityName := objectDisplayName(r.Context(), h.objects, ref)
	h.activitySvc.Record(r.Context(), u.ID, "file_renamed", ref, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   name,
	})

	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(200)
}

func multipartValue(r *http.Request, key string) (string, bool) {
	values := r.MultipartForm.Value[key]
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func detectUploadedFileMime(f io.ReadSeeker) (string, error) {
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("empty file")
	}
	return http.DetectContentType(buf[:n]), nil
}

func safeLocalRedirect(redirect string) string {
	if redirect == "" || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		return "/"
	}
	return redirect
}
