package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"os"

	"github.com/freefsm-project/freefsm/internal/ent/file"
)

type FileStorageIssue struct {
	FileID int64  `json:"file_id"`
	Reason string `json:"reason"`
}

// ReviewStorage is read-only: legacy discrepancies need human review, never
// fabricated upload history, silent removal, or rewriting recorded metadata.
func (s *FileService) ReviewStorage(ctx context.Context, company int64) ([]FileStorageIssue, error) {
	issues := []FileStorageIssue{}
	var last int64
	for {
		rows, err := s.client.File.Query().Where(file.CompanyIDEQ(company), file.IDGT(last)).Order(file.ByID()).Limit(100).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return issues, nil
		}
		for _, f := range rows {
			last = f.ID
			add := func(reason string) { issues = append(issues, FileStorageIssue{FileID: f.ID, Reason: reason}) }
			disk, err := os.Open(f.FilePath)
			if err != nil {
				add("durable_bytes_missing")
				continue
			}
			stat, err := disk.Stat()
			if err != nil || !stat.Mode().IsRegular() || stat.Size() != f.FileSize {
				disk.Close()
				add("recorded_size_mismatch")
				continue
			}
			h := sha256.New()
			_, err = io.Copy(h, disk)
			disk.Close()
			if err != nil {
				add("durable_bytes_unreadable")
				continue
			}
			if f.Sha256 == nil {
				add("legacy_digest_unrecorded")
			} else if *f.Sha256 != hex.EncodeToString(h.Sum(nil)) {
				add("recorded_digest_mismatch")
			}
			if detected, err := validateFileContent(f.FilePath, f.OriginalName); err != nil {
				add("content_extension_discrepancy")
			} else if recorded, _, err := mime.ParseMediaType(f.MimeType); err != nil || recorded != detected {
				add("recorded_media_type_mismatch")
			}
		}
	}
}
