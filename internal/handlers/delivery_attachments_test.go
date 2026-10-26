package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/google/uuid"
)

func TestDeliveryAttachmentLimitsAndContentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recipient").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Review").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, router, cookie, "GET", path+"/pdf/preview", 200), "version")
	pdf := requestBody(t, router, cookie, "GET", path+"/pdf?version="+version, 200)
	root := t.TempDir()
	sequence := 0
	queued := 0
	add := func(name, contentType string, data []byte, parent int64) string {
		sequence++
		disk := filepath.Join(root, fmt.Sprint(sequence))
		if err := os.WriteFile(disk, data, 0600); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'estimate',$2,$3,$4,$5,$6,$7,$8) RETURNING id`, company, parent, name, fmt.Sprint(sequence), contentType, len(data), disk, admin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(id)
	}
	submit := func(t *testing.T, ids []string, status int) {
		t.Helper()
		values := url.Values{"pdf_version": {version}, "idempotency_key": {uuid.NewString()}, "to": {"one@example.test, two@example.test"}, "subject": {"Reviewed"}, "body": {"Message"}, "extra_file_id": ids}
		fieldPost(t, router, cookie, path+"/email", values, status)
		if status == 303 {
			queued += 2
		}
		history, err := delivery.New(pool, "").History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: doc.ID})
		if err != nil || len(history) != queued {
			t.Fatalf("partial/absent fanout: %d want %d: %v", len(history), queued, err)
		}
	}
	t.Run("exact total raw bytes", func(t *testing.T) {
		submit(t, []string{add("boundary.txt", "text/plain", bytes.Repeat([]byte("a"), 15*1024*1024-len(pdf)), doc.ID)}, 303)
	})
	t.Run("one byte over", func(t *testing.T) {
		submit(t, []string{add("over.txt", "text/plain", bytes.Repeat([]byte("a"), 15*1024*1024-len(pdf)+1), doc.ID)}, 422)
	})
	var ten []string
	for range 10 {
		ten = append(ten, add("note.txt", "text/plain", []byte("note"), doc.ID))
	}
	t.Run("ten extras", func(t *testing.T) { submit(t, ten, 303) })
	t.Run("eleven extras", func(t *testing.T) {
		submit(t, append(append([]string{}, ten...), add("eleven.txt", "text/plain", []byte("note"), doc.ID)), 422)
	})
	other := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Other").SaveX(ctx)
	t.Run("accessible other document", func(t *testing.T) { submit(t, []string{add("other.txt", "text/plain", []byte("note"), other.ID)}, 404) })
	for _, tc := range []struct {
		name, mime string
		data       []byte
		status     int
	}{
		{"table.csv", "text/csv", []byte("name,value\nalpha,1\n"), 303},
		{"document.pdf", "application/pdf", []byte(pdf), 303},
		{"renamed.pdf", "application/pdf", []byte("not a PDF"), 422},
		{"program.exe", "application/octet-stream", []byte("MZ executable"), 422},
		{"script.txt", "text/plain", []byte("#!/bin/sh\necho unsafe"), 422},
		{"renamed-script.txt", "text/plain", []byte("alert('not plain correspondence');"), 422},
		{"markup.txt", "text/plain", []byte("<html><script>alert(1)</script></html>"), 422},
		{"archive.zip", "application/zip", deliveryOfficeFixture(t, "docx", false), 422},
		{"document.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", deliveryOfficeFixture(t, "docx", false), 303},
		{"sheet.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", deliveryOfficeFixture(t, "xlsx", false), 303},
		{"document.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeFixture(t, "odt", false), 303},
		{"sheet.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeFixture(t, "ods", false), 303},
		{"macro.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", deliveryOfficeFixture(t, "docx", true), 422},
		{"macro.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", deliveryOfficeFixture(t, "xlsx", true), 422},
		{"macro.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeFixture(t, "odt", true), 422},
		{"macro.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeFixture(t, "ods", true), 422},
		{"mismatch.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", deliveryOfficeFixture(t, "docx", false), 422},
		{"pixel.png", "image/png", deliveryImageFixture(t, "png"), 303},
		{"pixel.jpg", "image/jpeg", deliveryImageFixture(t, "jpeg"), 303},
		{"pixel.jpeg", "image/jpeg", deliveryImageFixture(t, "jpeg"), 303},
		{"pixel.gif", "image/gif", deliveryImageFixture(t, "gif"), 303},
		{"offset.gif", "image/gif", deliveryImageFixture(t, "gif-offset"), 303},
		{"pixel.webp", "image/webp", deliveryImageFixture(t, "webp"), 303},
		{"lossy-pixel.webp", "image/webp", deliveryImageFixture(t, "webp-lossy"), 303},
		{"mismatch.png", "image/png", deliveryImageFixture(t, "jpeg"), 422},
		{"wrong-mime.png", "image/jpeg", deliveryImageFixture(t, "png"), 422},
		{"forged.png", "image/png", []byte("\x89PNG\r\n\x1a\nnot a PNG"), 422},
		{"review-forged.webp", "image/webp", []byte("RIFF\x0c\x00\x00\x00WEBPnotimage"), 422},
		{"review-text-as-sheet.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "odt", map[string]string{
			"mimetype":              "application/vnd.oasis.opendocument.spreadsheet",
			"META-INF/manifest.xml": deliveryODFManifest("application/vnd.oasis.opendocument.spreadsheet"),
		}), 422},
		{"truncated-pixels.webp", "image/webp", deliveryWebPHeaderOnly(t, 1, 1), 422},
		{"oversized-canvas.webp", "image/webp", deliveryWebPHeaderOnly(t, 16384, 16384), 422},
		{"extended-pixel.webp", "image/webp", deliveryExtendedWebP(t, deliveryImageFixture(t, "webp"), 1, 1), 303},
		{"extended-lossy-pixel.webp", "image/webp", deliveryExtendedWebP(t, deliveryImageFixture(t, "webp-lossy"), 1, 1), 303},
		{"small-canvas-large-coded-image.webp", "image/webp", deliveryExtendedWebP(t, deliveryWebPHeaderOnly(t, 16384, 16384), 1, 1), 422},
		{"canvas-mismatch.webp", "image/webp", deliveryExtendedWebP(t, deliveryImageFixture(t, "webp"), 2, 1), 422},
		{"lossy-canvas-mismatch.webp", "image/webp", deliveryExtendedWebP(t, deliveryImageFixture(t, "webp-lossy"), 2, 1), 422},
		{"truncated-trailing-chunk.webp", "image/webp", deliveryWebPBrokenTail(t), 422},
		{"truncated-pixels.png", "image/png", deliveryImageFixture(t, "png")[:33], 422},
		{"truncated-pixels.jpg", "image/jpeg", deliveryImageFixture(t, "jpeg")[:100], 422},
		{"truncated-pixels.gif", "image/gif", deliveryImageFixture(t, "gif")[:13], 422},
		{"manifest-mismatch.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "ods", map[string]string{"META-INF/manifest.xml": deliveryODFManifest("application/vnd.oasis.opendocument.text")}), 422},
		{"sheet-as-text.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "ods", map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "META-INF/manifest.xml": deliveryODFManifest("application/vnd.oasis.opendocument.text")}), 422},
		{"mixed-bodies.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "ods", map[string]string{"content.xml": deliveryODFContent(`<office:spreadsheet/><office:text/>`)}), 422},
		{"wrong-body-namespace.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "ods", map[string]string{"content.xml": deliveryODFContent(`<wrong:spreadsheet xmlns:wrong="urn:wrong"/>`)}), 422},
		{"missing-body.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "ods", map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"/>`}), 422},
		{"malformed-body.ods", "application/vnd.oasis.opendocument.spreadsheet", deliveryOfficeVariant(t, "ods", map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:body><office:spreadsheet>`}), 422},
		{"empty-manifest.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"META-INF/manifest.xml": ""}), 422},
		{"duplicate-payload.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"content.xml": deliveryODFContent(`<office:text/><office:text/>`)}), 422},
		{"duplicate-office-body.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"content.xml": strings.Replace(deliveryODFContent(`<office:text><text:p>First</text:p></office:text>`), `</office:body>`, `</office:body><office:body><office:text><text:p>Second</text:p></office:text></office:body>`, 1)}), 422},
		{"wrong-manifest-namespace.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"META-INF/manifest.xml": strings.ReplaceAll(deliveryODFManifest("application/vnd.oasis.opendocument.text"), "urn:oasis:names:tc:opendocument:xmlns:manifest:1.0", "urn:wrong")}), 422},
		{"ambiguous-manifest.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"META-INF/manifest.xml": strings.Replace(deliveryODFManifest("application/vnd.oasis.opendocument.text"), `</manifest:manifest>`, `<manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.text"/></manifest:manifest>`, 1)}), 422},
		{"inline-script.odt", "application/vnd.oasis.opendocument.text", deliveryOfficeVariant(t, "odt", map[string]string{"content.xml": deliveryODFContent(`<office:text><office:script>unsafe</office:script></office:text>`)}), 422},
	} {
		t.Run(tc.name, func(t *testing.T) { submit(t, []string{add(tc.name, tc.mime, tc.data, doc.ID)}, tc.status) })
	}
}

func deliveryWebPHeaderOnly(t *testing.T, width, height uint32) []byte {
	t.Helper()
	data := append([]byte{}, deliveryImageFixture(t, "webp")[:25]...)
	data = append(data, 0)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	binary.LittleEndian.PutUint32(data[16:20], 5)
	binary.LittleEndian.PutUint32(data[21:25], (width-1)|((height-1)<<14))
	return data
}

func deliveryExtendedWebP(t *testing.T, coded []byte, width, height uint32) []byte {
	t.Helper()
	data := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00"), make([]byte, 10)...)
	data[24] = byte(width - 1)
	data[25] = byte((width - 1) >> 8)
	data[26] = byte((width - 1) >> 16)
	data[27] = byte(height - 1)
	data[28] = byte((height - 1) >> 8)
	data[29] = byte((height - 1) >> 16)
	data = append(data, coded[12:]...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}

func deliveryWebPBrokenTail(t *testing.T) []byte {
	t.Helper()
	data := append(deliveryImageFixture(t, "webp"), []byte("XMP \x10\x00\x00\x00oops")...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}

func deliveryOfficeVariant(t *testing.T, kind string, overrides map[string]string) []byte {
	t.Helper()
	data := deliveryOfficeFixture(t, kind, false)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	historyCheck(t, err)
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	seen := map[string]bool{}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		historyCheck(t, err)
		raw, err := io.ReadAll(reader)
		historyCheck(t, err)
		historyCheck(t, reader.Close())
		if replacement, ok := overrides[entry.Name]; ok {
			raw = []byte(replacement)
		}
		seen[entry.Name] = true
		part, err := writer.CreateHeader(&zip.FileHeader{Name: entry.Name, Method: entry.Method})
		historyCheck(t, err)
		_, err = part.Write(raw)
		historyCheck(t, err)
	}
	for name, value := range overrides {
		if !seen[name] {
			part, err := writer.Create(name)
			historyCheck(t, err)
			_, err = part.Write([]byte(value))
			historyCheck(t, err)
		}
	}
	historyCheck(t, writer.Close())
	return out.Bytes()
}

func deliveryImageFixture(t *testing.T, kind string) []byte {
	t.Helper()
	var b bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 255, A: 255})
	switch kind {
	case "png":
		historyCheck(t, png.Encode(&b, pixel))
	case "jpeg":
		historyCheck(t, jpeg.Encode(&b, pixel, nil))
	case "gif":
		historyCheck(t, gif.Encode(&b, pixel, nil))
	case "gif-offset":
		palette := color.Palette{color.White, color.Black}
		frame := image.NewPaletted(image.Rect(1, 1, 2, 2), palette)
		historyCheck(t, gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{ColorModel: palette, Width: 2, Height: 2}}))
	case "webp", "webp-lossy":
		encoded := "UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA="
		if kind == "webp-lossy" {
			// Generated from an owned 1x1 red pixel; no encoder dependency in CI.
			encoded = "UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoBAAEAAgA0JaACdLoB+AADsAD+8MQL/yC5YXXI1/8gP+QH/ID/+PIAAAA="
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		historyCheck(t, err)
		decoded, format, err := image.Decode(bytes.NewReader(data))
		historyCheck(t, err)
		if format != "webp" || decoded.Bounds().Dx() != 1 || decoded.Bounds().Dy() != 1 {
			t.Fatal("WebP positive fixture must decode as an actual 1x1 image")
		}
		return data
	}
	return b.Bytes()
}

func deliveryOfficeFixture(t *testing.T, kind string, macro bool) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	entries := map[string]string{}
	switch kind {
	case "docx":
		entries["[Content_Types].xml"] = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
		entries["_rels/.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`
		entries["word/document.xml"] = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Reviewed document</w:t></w:r></w:p></w:body></w:document>`
	case "xlsx":
		entries["[Content_Types].xml"] = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`
		entries["_rels/.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`
		entries["xl/workbook.xml"] = `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
		entries["xl/_rels/workbook.xml.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
		entries["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Reviewed cell</t></is></c></row></sheetData></worksheet>`
	case "odt":
		entries["mimetype"] = "application/vnd.oasis.opendocument.text"
		entries["content.xml"] = deliveryODFContent(`<office:text><text:p>Reviewed document</text:p></office:text>`)
	case "ods":
		entries["mimetype"] = "application/vnd.oasis.opendocument.spreadsheet"
		entries["content.xml"] = deliveryODFContent(`<office:spreadsheet><table:table table:name="Sheet1"><table:table-row><table:table-cell office:value-type="string"><text:p>Reviewed cell</text:p></table:table-cell></table:table-row></table:table></office:spreadsheet>`)
	}
	if media, ok := entries["mimetype"]; ok {
		entries["META-INF/manifest.xml"] = deliveryODFManifest(media)
	}
	if macro {
		switch kind {
		case "docx":
			entries["word/vbaProject.bin"] = "macro"
		case "xlsx":
			entries["xl/vbaProject.bin"] = "macro"
		default:
			entries["Basic/Standard/Module1.xml"] = "macro"
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		if name != "mimetype" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := entries["mimetype"]; ok {
		names = append([]string{"mimetype"}, names...)
	}
	for _, name := range names {
		data := entries[name]
		method := uint16(zip.Deflate)
		if name == "mimetype" {
			method = zip.Store
		}
		part, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func deliveryODFManifest(media string) string {
	return `<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3"><manifest:file-entry manifest:full-path="/" manifest:media-type="` + media + `" manifest:version="1.3"/><manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/></manifest:manifest>`
}

func deliveryODFContent(body string) string {
	return `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" office:version="1.3"><office:scripts/><office:body>` + body + `</office:body></office:document-content>`
}

func TestDeliveryAttachmentSnapshotHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recipient").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Review").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, router, cookie, "GET", path+"/pdf/preview", 200), "version")
	disk := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(disk, []byte("exact reviewed attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	var fileID int64
	if err := pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'estimate',$2,'note.txt','note.txt','text/plain',25,$3,$4) RETURNING id`, company, doc.ID, disk, admin).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"pdf_version": {version}, "idempotency_key": {uuid.NewString()}, "to": {"outside@example.test"}, "subject": {"Review"}, "body": {"Message"}, "extra_file_id": {fmt.Sprint(fileID)}}
	fieldPost(t, router, cookie, path+"/email", values, 303)
	if _, err := pool.Exec(ctx, `UPDATE document_deliveries SET attachment_snapshots='[]' WHERE document_id=$1`, doc.ID); err == nil {
		t.Fatal("attachment snapshots were mutable")
	}
	if _, err := pool.Exec(ctx, `UPDATE document_deliveries SET composition_snapshot='{}' WHERE document_id=$1`, doc.ID); err == nil {
		t.Fatal("composition snapshot was mutable")
	}
	if _, err := pool.Exec(ctx, `UPDATE files SET original_name='changed.txt',removed_at=now(),removed_by=$2 WHERE id=$1`, fileID, admin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("changed disk bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &compositionSender{}
	if worked, err := delivery.New(pool, "").ProcessOne(ctx, transport, nil); !worked || err != nil {
		t.Fatalf("worker: %v %v", worked, err)
	}
	extras := transport.messages[0].Attachments
	if len(extras) != 1 || extras[0].ID != fileID || extras[0].Name != "note.txt" || extras[0].ContentType != "text/plain" || string(extras[0].Data) != "exact reviewed attachment" {
		t.Fatalf("snapshot=%+v", extras)
	}
}
