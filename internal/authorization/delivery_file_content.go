package authorization

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gabriel-vasile/mimetype"
)

var deliveryScriptPrefix = regexp.MustCompile(`(?is)^\s*(?:(?:const|let|var)\s+[a-z_$][\w$]*\s*=|(?:async\s+)?function\s+\w+\s*\(|(?:alert|eval|exec|require|print|document\.write|console\.log)\s*\(|from\s+[\w.]+\s+import\b|import\s+[\w.]+\s*(?:;|$)|param\s*\(|(?:set-|invoke-)[\w-]+\b)`)
var deliveryActivePDFName = regexp.MustCompile(`/(?:JavaScript|JS|Launch|EmbeddedFile)(?:[\s<>\[\]()/]|$)`)

func validateDeliveryFile(f DeliveryFile) error {
	invalid := func() error {
		return &FileValidationError{Message: "unsupported, unsafe or mismatched attachment content"}
	}
	if f.Name == "" || len(f.Name) > 255 || strings.ContainsAny(f.Name, "/\\") || strings.ContainsFunc(f.Name, unicode.IsControl) {
		return invalid()
	}
	ext := strings.ToLower(filepath.Ext(f.Name))
	want := map[string]string{".pdf": "application/pdf", ".txt": "text/plain", ".csv": "text/csv", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".odt": "application/vnd.oasis.opendocument.text", ".ods": "application/vnd.oasis.opendocument.spreadsheet"}[ext]
	if want == "" || f.ContentType != want {
		return invalid()
	}
	if ext == ".docx" || ext == ".xlsx" || ext == ".odt" || ext == ".ods" {
		if !safeDeliveryOffice(f.Data, ext, want) {
			return invalid()
		}
		return nil
	}
	detected, _, err := mime.ParseMediaType(mimetype.Detect(f.Data).String())
	if err != nil {
		return invalid()
	}
	if ext == ".txt" || ext == ".csv" {
		text := strings.TrimSpace(string(f.Data))
		lower := strings.ToLower(text)
		if !utf8.Valid(f.Data) || strings.ContainsRune(text, 0) || (detected != "text/plain" && detected != "text/csv") || strings.HasPrefix(text, "#!") || strings.HasPrefix(lower, "@echo") || strings.HasPrefix(lower, "<?") || deliveryScriptPrefix.MatchString(text) || strings.HasPrefix(text, "MZ") {
			return invalid()
		}
		return nil
	}
	if detected != want {
		return invalid()
	}
	if ext == ".pdf" && deliveryActivePDFName.Match(f.Data) {
		return invalid()
	}
	if strings.HasPrefix(want, "image/") && ext != ".webp" {
		if _, _, err := image.DecodeConfig(bytes.NewReader(f.Data)); err != nil {
			return invalid()
		}
	}
	if ext == ".webp" {
		if len(f.Data) < 20 || string(f.Data[:4]) != "RIFF" || int64(binary.LittleEndian.Uint32(f.Data[4:8]))+8 != int64(len(f.Data)) {
			return invalid()
		}
	}
	return nil
}

// Office documents are ZIP containers, but general archives, embedded active
// content and macro-bearing variants are never mail attachments. Bound expanded
// inspection as well as raw size; no entry is extracted to disk.
func safeDeliveryOffice(data []byte, ext, want string) bool {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) > 1000 {
		return false
	}
	entries := map[string][]byte{}
	var expanded uint64
	for _, entry := range z.File {
		name := strings.ToLower(entry.Name)
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || entries[name] != nil {
			return false
		}
		if strings.Contains(name, "vba") || strings.Contains(name, "macro") || strings.Contains(name, "scripts/") || strings.Contains(name, "basic/") || strings.Contains(name, "embeddings/") || strings.Contains(name, "activex/") {
			return false
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".js", ".vbs", ".jar", ".zip":
			return false
		}
		expanded += entry.UncompressedSize64
		if expanded > 64*1024*1024 {
			return false
		}
		r, err := entry.Open()
		if err != nil {
			return false
		}
		body, err := io.ReadAll(io.LimitReader(r, 64*1024*1024+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil || len(body) > 64*1024*1024 {
			return false
		}
		if bytes.HasPrefix(body, []byte("MZ")) || bytes.HasPrefix(body, []byte("\x7fELF")) || bytes.HasPrefix(body, []byte("#!")) || bytes.HasPrefix(body, []byte("PK\x03\x04")) {
			return false
		}
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
			lower := strings.ToLower(string(body))
			for _, unsafe := range []string{"macroenabled", "vbaproject", "javascript:", "<!doctype", "<!entity"} {
				if strings.Contains(lower, unsafe) {
					return false
				}
			}
			decoder := xml.NewDecoder(bytes.NewReader(body))
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					return false
				}
				if start, ok := token.(xml.StartElement); ok {
					switch strings.ToLower(start.Name.Local) {
					case "script", "event-listener", "ddelink", "dde-connection", "oleobject":
						return false
					}
				}
			}
		}
		entries[name] = body
	}
	if ext == ".odt" || ext == ".ods" {
		return string(entries["mimetype"]) == want && deliveryXMLRoot(entries["content.xml"], "document-content", "urn:oasis:names:tc:opendocument:xmlns:office:1.0")
	}
	main, contentType := "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	if ext == ".xlsx" {
		main = "xl/workbook.xml"
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	}
	if ext == ".docx" && !deliveryXMLRoot(entries[main], "document", "http://schemas.openxmlformats.org/wordprocessingml/2006/main", "http://purl.oclc.org/ooxml/wordprocessingml/main") {
		return false
	}
	if ext == ".xlsx" && !deliveryXMLRoot(entries[main], "workbook", "http://schemas.openxmlformats.org/spreadsheetml/2006/main", "http://purl.oclc.org/ooxml/spreadsheetml/main") {
		return false
	}
	var types struct {
		Overrides []struct {
			PartName    string `xml:"PartName,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Override"`
	}
	if xml.Unmarshal(entries["[content_types].xml"], &types) != nil || len(entries[main]) == 0 {
		return false
	}
	for _, part := range types.Overrides {
		if part.PartName == "/"+main && part.ContentType == contentType {
			return true
		}
	}
	return false
}

func deliveryXMLRoot(body []byte, local string, namespaces ...string) bool {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok {
			if start.Name.Local != local {
				return false
			}
			for _, namespace := range namespaces {
				if start.Name.Space == namespace {
					return true
				}
			}
			return false
		}
	}
}
