package authorization

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

const MaxDeliveryAttachmentBytes = 15 * 1024 * 1024
const MaxDeliveryExtraFiles = 10

// DeliveryFile is a frozen copy, not a live File reference. The identity is
// provenance; retries never read that File again.
type DeliveryFile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}

type DeliveryFileChoice struct {
	ID                int64
	Name, ContentType string
	Bytes             int64
}

func (s *Service) DeliveryFileChoices(ctx context.Context, a Actor, kind string, id int64) ([]DeliveryFileChoice, error) {
	var out []DeliveryFileChoice
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := pdfDocumentAccess(ctx, tx, p, kind, id, "delivery.request"); err != nil {
			return err
		}
		ref, err := objectref.Parse(kind, id)
		if err != nil {
			return ErrNotFound
		}
		if _, err := fileAccess(ctx, tx, p, a, ref, "file.download", 0, false); err != nil {
			if errors.Is(err, ErrForbidden) {
				return nil
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,original_name,mime_type,file_size FROM files WHERE company_id=$1 AND object_type=$2 AND object_id=$3 AND removed_at IS NULL ORDER BY id`, p.CompanyID, kind, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f DeliveryFileChoice
			if err := rows.Scan(&f.ID, &f.Name, &f.ContentType, &f.Bytes); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// BindDeliveryFiles joins the outbox transaction and its authorization fence.
// Exact original-parent membership is checked before reading any durable bytes.
func (s *Service) BindDeliveryFiles(ctx context.Context, tx pgx.Tx, a Actor, kind string, id int64, ids []int64, pdfSize int) ([]DeliveryFile, error) {
	if len(ids) > MaxDeliveryExtraFiles || pdfSize > MaxDeliveryAttachmentBytes {
		return nil, &FileValidationError{Message: "at most 10 extra Files and 15 MiB total attachment bytes are allowed"}
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return nil, err
	}
	ref, err := objectref.Parse(kind, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if len(ids) > 0 {
		if _, err = fileAccess(ctx, tx, p, a, ref, "file.download", 0, true); err != nil {
			return nil, err
		}
	}
	out := make([]DeliveryFile, 0, len(ids))
	seen := map[int64]bool{}
	total := pdfSize
	for _, fileID := range ids {
		if fileID <= 0 || seen[fileID] {
			return nil, &FileValidationError{Message: "extra File identities must be distinct positive IDs"}
		}
		seen[fileID] = true
		var f DeliveryFile
		var path string
		err = tx.QueryRow(ctx, `SELECT id,original_name,mime_type,file_path FROM files WHERE id=$1 AND company_id=$2 AND object_type=$3 AND object_id=$4 AND removed_at IS NULL FOR SHARE`, fileID, p.CompanyID, kind, id).Scan(&f.ID, &f.Name, &f.ContentType, &path)
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		disk, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		stat, err := disk.Stat()
		if err != nil {
			disk.Close()
			return nil, err
		}
		if !stat.Mode().IsRegular() {
			disk.Close()
			return nil, &FileValidationError{Message: "attachment is not a regular File"}
		}
		f.Data, err = io.ReadAll(io.LimitReader(disk, int64(MaxDeliveryAttachmentBytes-total)+1))
		closeErr := disk.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		total += len(f.Data)
		if total > MaxDeliveryAttachmentBytes {
			return nil, &FileValidationError{Message: "total attachment bytes exceed 15 MiB"}
		}
		if err = validateDeliveryFile(f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}
