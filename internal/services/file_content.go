package services

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/gabriel-vasile/mimetype"
)

// validateFileContent derives media type from durable bytes, never a request's
// Content-Type. Archive inspection does not extract entries onto the filesystem.
func validateFileContent(path, name string) (string, error) {
	if strings.TrimSpace(name) == "" || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return "", invalidFile("invalid file content: invalid display name")
	}
	ext := strings.ToLower(filepath.Ext(name))
	want := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
		".pdf": "application/pdf", ".txt": "text/plain", ".csv": "text/csv", ".json": "application/json", ".zip": "application/zip",
		".doc": "application/msword", ".xls": "application/vnd.ms-excel", ".ppt": "application/vnd.ms-powerpoint",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".odt":  "application/vnd.oasis.opendocument.text", ".ods": "application/vnd.oasis.opendocument.spreadsheet",
	}[ext]
	if want == "" {
		return "", invalidFile("invalid file content: unsupported extension")
	}
	if ext == ".doc" || ext == ".xls" || ext == ".ppt" {
		got, err := legacyOfficeType(path)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", invalidFile("invalid file content: content does not match extension")
		}
		return got, nil
	}
	if ext == ".zip" || ext == ".docx" || ext == ".xlsx" || ext == ".pptx" || ext == ".odt" || ext == ".ods" {
		archive, err := zip.OpenReader(path)
		if err != nil {
			if errors.Is(err, zip.ErrFormat) {
				return "", invalidFile("invalid file content: malformed ZIP container")
			}
			return "", err
		}
		defer archive.Close()
		// The central directory is read only. Never extract ZIP entries, even
		// when they contain absolute paths or traversal names.
		if ext == ".zip" {
			return want, nil
		}
	}
	detected, err := mimetype.DetectFile(path)
	if err != nil {
		return "", fmt.Errorf("validate file content: %w", err)
	}
	got, _, err := mime.ParseMediaType(detected.String())
	if err != nil {
		return "", err
	}
	valid := got == want
	if ext == ".txt" || ext == ".csv" {
		valid = detected.Is("text/plain") || detected.Parent() != nil && detected.Parent().Is("text/plain")
	}
	if ext == ".json" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		valid = json.Valid(data)
	}
	if !valid {
		return "", invalidFile("invalid file content: content does not match extension")
	}
	return want, nil
}
