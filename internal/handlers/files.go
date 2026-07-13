package handlers

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

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

	ref, err := uploadTargetFromQuery(r)
	if err != nil || !h.svc.SupportsFiles(ref) {
		http.Error(w, "Invalid attachment target", http.StatusBadRequest)
		return
	}
	if !h.svc.TargetExists(r.Context(), u.CompanyID, ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.svc.MaxSize()+1024*1024)
	if err := r.ParseMultipartForm(h.svc.MaxSize()); err != nil {
		http.Error(w, "File too large", 413)
		return
	}
	defer r.MultipartForm.RemoveAll()

	fh, err := oneUploadedFile(r.MultipartForm)
	if err != nil {
		http.Error(w, err.Error(), 400)
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

	fileReader := http.MaxBytesReader(w, f, h.svc.MaxSize())
	defer fileReader.Close()
	_, err = h.svc.Create(r.Context(), u.CompanyID, ref, fh.Filename, mimeType, fh.Size, fileReader, u.ID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid MIME type:") {
			http.Error(w, "Invalid file type", 400)
			return
		}
		if strings.HasPrefix(err.Error(), "file size mismatch:") {
			http.Error(w, "Invalid file size", http.StatusBadRequest)
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
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "file_uploaded", ref, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   fh.Filename,
	})

	redirect, _ := multipartValue(r, "redirect")
	redirect = safeLocalRedirect(redirect)
	writeUploadSuccess(w, r, redirect)
}

func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExistsAny(r.Context(), u.CompanyID, ref) {
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
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid file ID", 400)
		return
	}

	f, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExists(r.Context(), u.CompanyID, ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyDelete) && !(f.UploadedBy == u.ID && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile)) {
		http.Error(w, "Forbidden", 403)
		return
	}

	redirect := safeLocalRedirect(r.FormValue("redirect"))

	if err := h.svc.Delete(r.Context(), u.CompanyID, id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	entityName := objectDisplayName(r.Context(), h.objects, ref)
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "file_deleted", ref, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   f.OriginalName,
	})

	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(200)
}

func (h *FileHandler) Rename(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid file ID", 400)
		return
	}

	f, err := h.svc.GetByID(r.Context(), u.CompanyID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || !h.svc.TargetExists(r.Context(), u.CompanyID, ref) {
		http.NotFound(w, r)
		return
	}
	if !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyDelete) && !(f.UploadedBy == u.ID && h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyAttachFile)) {
		http.Error(w, "Forbidden", 403)
		return
	}

	redirect := safeLocalRedirect(r.FormValue("redirect"))
	name := strings.TrimSpace(r.FormValue("original_name"))
	if err := h.svc.Rename(r.Context(), u.CompanyID, id, name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	entityName := objectDisplayName(r.Context(), h.objects, ref)
	h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "file_renamed", ref, map[string]interface{}{
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

func uploadTargetFromQuery(r *http.Request) (objectref.Ref, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return objectref.Ref{}, err
	}
	objectTypes := values["object_type"]
	objectIDs := values["object_id"]
	if len(objectTypes) != 1 || objectTypes[0] == "" || len(objectIDs) != 1 {
		return objectref.Ref{}, fmt.Errorf("object_type and object_id query parameters are required exactly once")
	}
	objectID, err := strconv.ParseInt(objectIDs[0], 10, 64)
	if err != nil {
		return objectref.Ref{}, fmt.Errorf("invalid object_id: %w", err)
	}
	return objectref.Parse(objectTypes[0], objectID)
}

func oneUploadedFile(form *multipart.Form) (*multipart.FileHeader, error) {
	fileParts := 0
	for _, headers := range form.File {
		fileParts += len(headers)
	}
	files := form.File["file"]
	if fileParts == 0 || len(files) == 0 {
		return nil, fmt.Errorf("No file uploaded")
	}
	if fileParts != 1 || len(files) != 1 {
		return nil, fmt.Errorf("Only one file may be uploaded")
	}
	return files[0], nil
}

func writeUploadSuccess(w http.ResponseWriter, r *http.Request, redirect string) {
	if strings.EqualFold(r.Header.Get("HX-Request"), "true") {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
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
	if redirect == "" || unsafeRedirectText(redirect) {
		return "/"
	}
	decoded := redirect
	for range 4 {
		next, err := url.PathUnescape(decoded)
		if err != nil || unsafeRedirectText(next) {
			return "/"
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	parsed, err := url.Parse(redirect)
	if err != nil || parsed.IsAbs() || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "/"
	}
	if !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return "/"
	}
	return redirect
}

func unsafeRedirectText(value string) bool {
	if strings.Contains(value, `\`) || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
