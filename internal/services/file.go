package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/file"
	"github.com/freefsm-project/freefsm/internal/ent/user"
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

func (s *FileService) TargetExists(ctx context.Context, companyID int64, ref objectref.Ref) bool {
	return s.validateTarget(ctx, companyID, ref, objectref.ExistsActive) == nil
}

func (s *FileService) TargetExistsAny(ctx context.Context, companyID int64, ref objectref.Ref) bool {
	return s.validateTarget(ctx, companyID, ref, objectref.ExistsAny) == nil
}

func (s *FileService) MaxSize() int64 {
	return s.maxSize
}

func (s *FileService) UploadDir() string {
	return s.uploadDir
}

func (s *FileService) List(ctx context.Context, companyID int64, ref objectref.Ref) ([]*ent.File, error) {
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}

	return s.client.File.Query().
		Where(file.CompanyIDEQ(companyID), file.ObjectType(ref.ObjectType()), file.ObjectID(ref.ObjectID())).
		Order(ent.Desc(file.FieldCreatedAt), ent.Desc(file.FieldID)).
		All(ctx)
}

func (s *FileService) GetByID(ctx context.Context, companyID, id int64) (*ent.File, error) {
	if err := validateFileCompanyID(companyID); err != nil {
		return nil, err
	}
	f, err := s.client.File.Query().Where(file.IDEQ(id), file.CompanyIDEQ(companyID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get file %d: %w", id, err)
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || s.validateTarget(ctx, companyID, ref, objectref.ExistsAny) != nil {
		return nil, fmt.Errorf("get file %d: %w", id, &ent.NotFoundError{})
	}
	return f, nil
}

func (s *FileService) Create(ctx context.Context, companyID int64, ref objectref.Ref, originalName string, mimeType string, fileSize int64, reader io.Reader, uploadedBy int64) (*ent.File, error) {
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsActive); err != nil {
		return nil, err
	}
	if fileSize < 0 {
		return nil, fmt.Errorf("file size %d cannot be negative", fileSize)
	}
	if uploadedBy <= 0 {
		return nil, fmt.Errorf("invalid uploaded by user ID: %d", uploadedBy)
	}
	uploaderExists, err := s.client.User.Query().Where(user.IDEQ(uploadedBy), user.CompanyIDEQ(companyID)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("validate uploaded by user: %w", err)
	}
	if !uploaderExists {
		return nil, fmt.Errorf("uploaded by user %d not found", uploadedBy)
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
	cleanup := func() error {
		closeErr := f.Close()
		removeErr := os.Remove(filePath)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		return errors.Join(closeErr, removeErr)
	}
	failWrite := func(err error) (*ent.File, error) {
		return nil, errors.Join(err, cleanup())
	}

	copyLimit := s.maxSize
	if copyLimit < math.MaxInt64 {
		copyLimit++
	}
	actualSize, err := io.Copy(f, io.LimitReader(reader, copyLimit))
	if err != nil {
		return failWrite(fmt.Errorf("write file to disk: %w", err))
	}
	if actualSize > s.maxSize {
		return failWrite(fmt.Errorf("file size %d exceeds maximum %d", actualSize, s.maxSize))
	}
	if actualSize != fileSize {
		return failWrite(fmt.Errorf("file size mismatch: declared %d, read %d", fileSize, actualSize))
	}
	if err := f.Close(); err != nil {
		removeErr := os.Remove(filePath)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		return nil, errors.Join(fmt.Errorf("close file on disk: %w", err), removeErr)
	}

	entFile, err := s.client.File.Create().
		SetCompanyID(companyID).
		SetObjectType(ref.ObjectType()).
		SetObjectID(ref.ObjectID()).
		SetOriginalName(originalName).
		SetStoredName(storedName).
		SetMimeType(mimeType).
		SetFileSize(actualSize).
		SetFilePath(filePath).
		SetUploadedBy(uploadedBy).
		Save(ctx)
	if err != nil {
		removeErr := os.Remove(filePath)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		return nil, errors.Join(fmt.Errorf("create file record: %w", err), removeErr)
	}

	return entFile, nil
}

func (s *FileService) CreateBytes(ctx context.Context, companyID int64, ref objectref.Ref, originalName string, mimeType string, data []byte, uploadedBy int64) (*ent.File, error) {
	return s.Create(ctx, companyID, ref, originalName, mimeType, int64(len(data)), bytes.NewReader(data), uploadedBy)
}

func (s *FileService) validateTarget(ctx context.Context, companyID int64, ref objectref.Ref, mode objectref.ExistenceMode) error {
	if err := validateFileCompanyID(companyID); err != nil {
		return err
	}
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
	targetCompanyID, err := s.objects.TargetCompanyID(ctx, ref)
	if err != nil || targetCompanyID != companyID {
		return fmt.Errorf("target %s %d not found", ref.ObjectType(), ref.ObjectID())
	}
	return nil
}

func (s *FileService) Delete(ctx context.Context, companyID, id int64) error {
	f, err := s.GetByID(ctx, companyID, id)
	if err != nil {
		return err
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || s.validateTarget(ctx, companyID, ref, objectref.ExistsActive) != nil {
		return fmt.Errorf("delete file %d: %w", id, &ent.NotFoundError{})
	}

	if err := os.Remove(f.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file from disk: %w", err)
	}

	deleted, err := s.client.File.Delete().Where(file.IDEQ(id), file.CompanyIDEQ(companyID)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}
	if deleted == 0 {
		return fmt.Errorf("delete file %d: %w", id, &ent.NotFoundError{})
	}
	return nil
}

func (s *FileService) Rename(ctx context.Context, companyID, id int64, originalName string) error {
	if err := validateFileCompanyID(companyID); err != nil {
		return err
	}
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

	f, err := s.GetByID(ctx, companyID, id)
	if err != nil {
		return err
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || s.validateTarget(ctx, companyID, ref, objectref.ExistsActive) != nil {
		return fmt.Errorf("rename file %d: %w", id, &ent.NotFoundError{})
	}
	updated, err := s.client.File.Update().Where(file.IDEQ(id), file.CompanyIDEQ(companyID)).SetOriginalName(originalName).Save(ctx)
	if err != nil {
		return fmt.Errorf("rename file %d: %w", id, err)
	}
	if updated == 0 {
		return fmt.Errorf("rename file %d: %w", id, &ent.NotFoundError{})
	}
	return nil
}

func validateFileCompanyID(companyID int64) error {
	if companyID <= 0 {
		return fmt.Errorf("company ID must be positive")
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
