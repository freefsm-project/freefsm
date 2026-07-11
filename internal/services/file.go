package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/file"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
)

var allowedMIMETypes = []string{
	"image/png",
	"image/jpeg",
	"image/gif",
	"application/pdf",
	"text/plain; charset=utf-8",
	"text/plain",
	"application/msword",
	"application/vnd.ms-excel",
	"application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/zip",
	"application/json",
}

type FileService struct {
	client    *ent.Client
	objects   objectref.Directory
	uploadDir string
	maxSize   int64
}

func NewFileService(client *ent.Client, objects objectref.Directory, uploadDir string, maxSize int64) *FileService {
	return &FileService{client: client, objects: objects, uploadDir: uploadDir, maxSize: maxSize}
}

func (s *FileService) ValidateMIMEType(mimeType string) bool {
	for _, allowed := range allowedMIMETypes {
		if mimeType == allowed {
			return true
		}
	}
	return false
}

func (s *FileService) SupportsFiles(ref objectref.Ref) bool {
	return ref.Valid() && s.objects.Supports(ref.Type, objectref.CapFiles)
}

func (s *FileService) TargetExists(ctx context.Context, ref objectref.Ref) bool {
	if !s.SupportsFiles(ref) {
		return false
	}
	exists, err := s.objects.Exists(ctx, ref, objectref.ExistsActive)
	return err == nil && exists
}

func (s *FileService) TargetExistsAny(ctx context.Context, ref objectref.Ref) bool {
	if !s.SupportsFiles(ref) {
		return false
	}
	exists, err := s.objects.Exists(ctx, ref, objectref.ExistsAny)
	return err == nil && exists
}

func (s *FileService) MaxSize() int64 {
	return s.maxSize
}

func (s *FileService) UploadDir() string {
	return s.uploadDir
}

func (s *FileService) List(ctx context.Context, ref objectref.Ref) ([]*ent.File, error) {
	if err := s.validateTarget(ctx, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}

	return s.client.File.Query().
		Where(file.ObjectType(ref.ObjectType()), file.ObjectID(ref.ObjectID())).
		Order(ent.Desc(file.FieldCreatedAt)).
		All(ctx)
}

func (s *FileService) GetByID(ctx context.Context, id int64) (*ent.File, error) {
	f, err := s.client.File.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get file %d: %w", id, err)
	}
	return f, nil
}

func (s *FileService) Create(ctx context.Context, ref objectref.Ref, originalName string, mimeType string, fileSize int64, reader io.Reader, uploadedBy int64) (*ent.File, error) {
	if err := s.validateTarget(ctx, ref, objectref.ExistsActive); err != nil {
		return nil, err
	}
	if !s.ValidateMIMEType(mimeType) {
		return nil, fmt.Errorf("invalid MIME type: %s", mimeType)
	}
	if fileSize > s.maxSize {
		return nil, fmt.Errorf("file size %d exceeds maximum %d", fileSize, s.maxSize)
	}

	ext := strings.ToLower(filepath.Ext(originalName))
	storedName := uuid.New().String() + ext

	now := time.Now()
	subDir := filepath.Join(s.uploadDir, fmt.Sprintf("%d", now.Year()), fmt.Sprintf("%02d", now.Month()))
	if err := os.MkdirAll(subDir, 0750); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}

	filePath := filepath.Join(subDir, storedName)
	f, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("create file on disk: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		return nil, fmt.Errorf("write file to disk: %w", err)
	}

	entFile, err := s.client.File.Create().
		SetObjectType(ref.ObjectType()).
		SetObjectID(ref.ObjectID()).
		SetOriginalName(originalName).
		SetStoredName(storedName).
		SetMimeType(mimeType).
		SetFileSize(fileSize).
		SetFilePath(filePath).
		SetUploadedBy(uploadedBy).
		Save(ctx)
	if err != nil {
		_ = os.Remove(filePath)
		return nil, fmt.Errorf("create file record: %w", err)
	}

	return entFile, nil
}

func (s *FileService) CreateBytes(ctx context.Context, ref objectref.Ref, originalName string, mimeType string, data []byte, uploadedBy int64) (*ent.File, error) {
	return s.Create(ctx, ref, originalName, mimeType, int64(len(data)), bytes.NewReader(data), uploadedBy)
}

func (s *FileService) validateTarget(ctx context.Context, ref objectref.Ref, mode objectref.ExistenceMode) error {
	if !s.SupportsFiles(ref) {
		return fmt.Errorf("invalid object type: %s", ref.ObjectType())
	}
	exists, err := s.objects.Exists(ctx, ref, mode)
	if err != nil {
		return fmt.Errorf("validate attachment target: %w", err)
	}
	if !exists {
		return fmt.Errorf("target %s %d not found", ref.ObjectType(), ref.ObjectID())
	}
	return nil
}

func (s *FileService) Delete(ctx context.Context, id int64) error {
	f, err := s.client.File.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get file %d: %w", id, err)
	}

	if err := os.Remove(f.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file from disk: %w", err)
	}

	if err := s.client.File.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}
	return nil
}

func (s *FileService) Rename(ctx context.Context, id int64, originalName string) error {
	originalName = strings.TrimSpace(originalName)
	if originalName == "" {
		return fmt.Errorf("file name is required")
	}
	if len(originalName) > 255 {
		return fmt.Errorf("file name is too long")
	}
	if strings.ContainsAny(originalName, "/\\") {
		return fmt.Errorf("file name cannot contain path separators")
	}

	if err := s.client.File.UpdateOneID(id).SetOriginalName(originalName).Exec(ctx); err != nil {
		return fmt.Errorf("rename file %d: %w", id, err)
	}
	return nil
}

func (s *FileService) GetDiskPath(storedName string) string {
	return filepath.Join(s.uploadDir, storedName)
}

func IsInlineMimeType(mimeType string) bool {
	return mimeType == "image/png" || mimeType == "image/jpeg" || mimeType == "image/gif" || mimeType == "application/pdf"
}

func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func FormatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
