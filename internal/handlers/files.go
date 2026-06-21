package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

type FileHandler struct {
	svc         *services.FileService
	activitySvc *services.ActivityService
}

func NewFileHandler(svc *services.FileService, activitySvc *services.ActivityService) *FileHandler {
	return &FileHandler{svc: svc, activitySvc: activitySvc}
}

func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

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
	objectType := r.MultipartForm.Value["object_type"][0]
	objectID, _ := strconv.ParseInt(r.MultipartForm.Value["object_id"][0], 10, 64)

	f, err := fh.Open()
	if err != nil {
		http.Error(w, "Cannot open file", 500)
		return
	}
	defer f.Close()

	mimeType := fh.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	_, err = h.svc.Create(r.Context(), objectType, objectID, fh.Filename, mimeType, fh.Size, f, u.ID)
	if err != nil {
		if err.Error() == fmt.Sprintf("invalid MIME type: %s", mimeType) {
			http.Error(w, "Invalid file type", 400)
			return
		}
		if err.Error() == fmt.Sprintf("file size %d exceeds maximum %d", fh.Size, h.svc.MaxSize()) {
			http.Error(w, "File too large", 413)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}

	entityName := h.activitySvc.LookupEntityName(r.Context(), objectType, objectID)
	h.activitySvc.Record(r.Context(), u.ID, "file_uploaded", objectType, objectID, map[string]interface{}{
		"entity_name": entityName,
		"actor_name":  u.Name,
		"file_name":   fh.Filename,
	})

	redirect := r.MultipartForm.Value["redirect"][0]
	if redirect == "" {
		redirect = "/"
	}
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

	data, err := services.ReadFile(f.FilePath)
	if err != nil {
		http.Error(w, "Cannot read file", 500)
		return
	}

	if services.IsInlineMimeType(f.MimeType) {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.OriginalName))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	}
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.FileSize, 10))
	w.Write(data)
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

	redirect := r.FormValue("redirect")
	if redirect == "" {
		redirect = "/"
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		entityName := h.activitySvc.LookupEntityName(r.Context(), f.ObjectType, f.ObjectID)
		h.activitySvc.Record(r.Context(), u.ID, "file_deleted", f.ObjectType, f.ObjectID, map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
			"file_name":   f.OriginalName,
		})
	}

	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(200)
}
