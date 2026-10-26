package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
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
		{"pixel.webp", "image/webp", deliveryImageFixture(t, "webp"), 303},
		{"mismatch.png", "image/png", deliveryImageFixture(t, "jpeg"), 422},
		{"wrong-mime.png", "image/jpeg", deliveryImageFixture(t, "png"), 422},
		{"forged.png", "image/png", []byte("\x89PNG\r\n\x1a\nnot a PNG"), 422},
	} {
		t.Run(tc.name, func(t *testing.T) { submit(t, []string{add(tc.name, tc.mime, tc.data, doc.ID)}, tc.status) })
	}
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
	case "webp":
		data, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA=")
		historyCheck(t, err)
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
		entries["[Content_Types].xml"] = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
		entries["word/document.xml"] = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`
	case "xlsx":
		entries["[Content_Types].xml"] = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`
		entries["xl/workbook.xml"] = `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`
	case "odt":
		entries["mimetype"] = "application/vnd.oasis.opendocument.text"
		entries["content.xml"] = `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:script="urn:oasis:names:tc:opendocument:xmlns:script:1.0"><office:scripts/><office:body><office:text/></office:body></office:document-content>`
	case "ods":
		entries["mimetype"] = "application/vnd.oasis.opendocument.spreadsheet"
		entries["content.xml"] = `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:body><office:spreadsheet/></office:body></office:document-content>`
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
	for name, data := range entries {
		part, err := w.Create(name)
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
