package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/file"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var allowedMIMETypes = []string{
	"image/png",
	"image/jpeg",
	"image/gif",
	"image/webp",
	"application/pdf",
	"text/plain; charset=utf-8",
	"text/plain",
	"text/csv",
	"application/msword",
	"application/vnd.ms-excel",
	"application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/zip",
	"application/json",
	"application/vnd.oasis.opendocument.text",
	"application/vnd.oasis.opendocument.spreadsheet",
}

type FileService struct {
	client    *ent.Client
	objects   objectref.Directory
	uploadDir string
	maxSize   int64
}

func NewFileService(client *ent.Client, objects objectref.Directory, uploadDir string, maxSize int64) *FileService {
	if maxSize <= 0 {
		maxSize = 25 << 20
	}
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
	if request, ok := authorization.FromContext(ctx); ok {
		if err := request.Service.FileAccess(ctx, request.Actor, ref, "", 0); err != nil {
			return nil, err
		}
	}
	if err := s.validateTarget(ctx, companyID, ref, objectref.ExistsAny); err != nil {
		return nil, err
	}

	return s.client.File.Query().
		Where(file.CompanyIDEQ(companyID), file.ObjectType(ref.ObjectType()), file.ObjectID(ref.ObjectID()), file.RemovedAtIsNil()).
		Order(ent.Desc(file.FieldCreatedAt), ent.Desc(file.FieldID)).
		All(ctx)
}

func (s *FileService) GetByID(ctx context.Context, companyID, id int64) (*ent.File, error) {
	f, err := s.Metadata(ctx, companyID, id)
	if err != nil {
		return nil, err
	}
	if f.RemovedAt != nil {
		return nil, &ent.NotFoundError{}
	}
	return f, nil
}

// Metadata retains removal attribution but grants no access to removed bytes.
func (s *FileService) Metadata(ctx context.Context, companyID, id int64) (*ent.File, error) {
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
	if request, ok := authorization.FromContext(ctx); ok {
		if err := request.Service.FileAccess(ctx, request.Actor, ref, "", f.UploadedBy); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// FilePublication is the stable publication identity, independent of transfer
// attempts. Digest, when supplied, is SHA-256 of the exact transmitted bytes.
type FilePublication struct {
	ID         string
	Digest     string
	OccurredAt *time.Time
}

func (s *FileService) Create(ctx context.Context, companyID int64, ref objectref.Ref, originalName string, mimeType string, fileSize int64, reader io.Reader, uploadedBy int64, publication ...FilePublication) (result *ent.File, resultErr error) {
	identity := FilePublication{ID: uuid.NewString()}
	if len(publication) > 0 {
		identity = publication[0]
	}
	if request, ok := authorization.FromContext(ctx); ok && request.Actor.Channel == "api" {
		defer func() {
			if resultErr != nil {
				resultErr = request.Service.RejectFilePublication(ctx, request.Actor, identity.ID, filePublicationIntent(ref, originalName, fileSize, identity.Digest, identity.OccurredAt), resultErr)
			}
		}()
	}
	if _, err := uuid.Parse(identity.ID); err != nil {
		return nil, fmt.Errorf("invalid file content: publication ID must be a UUID")
	}
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
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	digest := sha256.New()
	actualSize, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(reader, copyLimit))
	if err != nil {
		return failWrite(fmt.Errorf("write file to disk: %w", err))
	}
	if actualSize > s.maxSize {
		return failWrite(fmt.Errorf("file size %d exceeds maximum %d", actualSize, s.maxSize))
	}
	if actualSize != fileSize {
		return failWrite(fmt.Errorf("file size mismatch: declared %d, read %d", fileSize, actualSize))
	}
	if identity.Digest != "" && identity.Digest != hex.EncodeToString(digest.Sum(nil)) {
		return failWrite(fmt.Errorf("invalid file content: digest mismatch"))
	}
	mimeType, err = validateFileContent(filePath, originalName)
	if err != nil {
		return failWrite(err)
	}
	if err := f.Sync(); err != nil {
		return failWrite(fmt.Errorf("sync file to disk: %w", err))
	}
	if err := f.Close(); err != nil {
		removeErr := os.Remove(filePath)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		return nil, errors.Join(fmt.Errorf("close file on disk: %w", err), removeErr)
	}

	// fsync the directory entry before committing its database reference.
	for _, path := range []string{subDir, filepath.Dir(subDir), s.uploadDir} {
		var dir *os.File
		dir, err = os.Open(path)
		if err != nil {
			break
		}
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			break
		}
	}
	if err != nil {
		return nil, errors.Join(err, os.Remove(filePath))
	}
	var entFile *ent.File
	replayed := false
	if request, ok := authorization.FromContext(ctx); ok {
		err = request.Service.FileAction(ctx, request.Actor, ref, "file.upload", uploadedBy, func(tx pgx.Tx) (int64, any, error) {
			clientKey, err := publicationClientKey(ctx, tx, request.Actor)
			if err != nil {
				return 0, nil, err
			}
			var encoded []byte
			var mutated bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_mutation_operations WHERE user_id=$1 AND operation_id=$2)`, request.Actor.UserID, identity.ID).Scan(&mutated); err != nil {
				return 0, nil, err
			}
			if mutated {
				return 0, nil, authorization.ErrConflict
			}
			intentFields := filePublicationIntent(ref, originalName, actualSize, hex.EncodeToString(digest.Sum(nil)), identity.OccurredAt)
			intentBytes, _ := json.Marshal(intentFields)
			intent := string(intentBytes)
			if err := authorization.AdmitOperation(ctx, tx, request.Actor, identity.ID, "file.publication", json.RawMessage(intentBytes)); err != nil {
				return 0, nil, err
			}
			if err := authorization.FilePublicationRejection(ctx, tx, identity.ID); err != nil {
				return 0, nil, err
			}
			var prior string
			var priorClient *string
			err = tx.QueryRow(ctx, `SELECT p.intent,p.client_key,to_jsonb(f) FROM file_publications p JOIN files f ON f.id=p.file_id WHERE p.user_id=$1 AND p.operation_id=$2`, request.Actor.UserID, identity.ID).Scan(&prior, &priorClient, &encoded)
			if err == nil {
				if prior != intent || priorClient == nil || *priorClient != clientKey {
					return 0, nil, authorization.ErrConflict
				}
				entFile = &ent.File{}
				replayed = true
				return 0, nil, json.Unmarshal(encoded, entFile)
			}
			if err != pgx.ErrNoRows {
				return 0, nil, err
			}
			err = tx.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by,sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING to_jsonb(files)`, companyID, ref.ObjectType(), ref.ID, originalName, storedName, mimeType, actualSize, filePath, request.Actor.UserID, hex.EncodeToString(digest.Sum(nil))).Scan(&encoded)
			if err != nil {
				return 0, nil, err
			}
			entFile = &ent.File{}
			if err := json.Unmarshal(encoded, entFile); err != nil {
				return 0, nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO file_publications(user_id,operation_id,intent,file_id,client_key) VALUES($1,$2,$3,$4,$5)`, request.Actor.UserID, identity.ID, intent, entFile.ID, clientKey); err != nil {
				return 0, nil, err
			}
			return entFile.ID, map[string]any{"parent_type": ref.ObjectType(), "parent_id": ref.ID, "name": originalName, "size": actualSize, "publication_id": identity.ID}, nil
		}, authorization.FileOperation{OperationID: identity.ID, OccurredAt: identity.OccurredAt})
	} else {
		entFile, err = s.client.File.Create().
			SetCompanyID(companyID).
			SetObjectType(ref.ObjectType()).
			SetObjectID(ref.ObjectID()).
			SetOriginalName(originalName).
			SetStoredName(storedName).
			SetMimeType(mimeType).
			SetFileSize(actualSize).
			SetFilePath(filePath).
			SetUploadedBy(uploadedBy).
			SetSha256(hex.EncodeToString(digest.Sum(nil))).
			Save(ctx)
	}
	if err != nil {
		// Commit may have reached PostgreSQL even when its response is lost.
		// Never remove durable bytes after attempting publication: an orphan
		// is recoverable; deleting a committed File's bytes is not.
		return nil, fmt.Errorf("create file record: %w", err)
	}
	if replayed {
		if err := os.Remove(filePath); err != nil {
			return nil, err
		}
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

func (s *FileService) Delete(ctx context.Context, companyID, id int64, revision ...int64) error {
	if request, ok := authorization.FromContext(ctx); ok {
		return s.mutateFile(ctx, request, companyID, id, "file.remove", "", revision)
	}
	f, err := s.GetByID(ctx, companyID, id)
	if err != nil {
		return err
	}
	ref, err := objectref.Parse(f.ObjectType, f.ObjectID)
	if err != nil || s.validateTarget(ctx, companyID, ref, objectref.ExistsActive) != nil {
		return fmt.Errorf("delete file %d: %w", id, &ent.NotFoundError{})
	}

	return authorization.ErrForbidden
}

func (s *FileService) Rename(ctx context.Context, companyID, id int64, originalName string, revision ...int64) error {
	if request, ok := authorization.FromContext(ctx); ok {
		return s.mutateFile(ctx, request, companyID, id, "file.rename", strings.TrimSpace(originalName), revision)
	}
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
	if filepath.Ext(originalName) != filepath.Ext(f.OriginalName) {
		return fmt.Errorf("file extension must be preserved")
	}
	return authorization.ErrForbidden
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
	return mimeType == "image/png" || mimeType == "image/jpeg" || mimeType == "image/gif" || mimeType == "image/webp" || mimeType == "application/pdf"
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
