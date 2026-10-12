package handlers

import (
	"bytes"
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLegacyOfficeCorpusHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy Office corpus").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	corpus := []struct{ name, mime string }{
		{"document.doc", "application/msword"},
		{"workbook.xls", "application/vnd.ms-excel"},
		{"slides.ppt", "application/vnd.ms-powerpoint"},
	}
	for _, fixture := range corpus {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "files", "legacy-office", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			w := uploadFileHTTP(t, router, cookie, c.ID, fixture.name, data)
			if w.Code != 200 {
				t.Fatalf("valid real Office binary rejected: %d %s", w.Code, w.Body.String())
			}
			id := w.Header().Get("File-ID")
			r := httptest.NewRequest("GET", "/files/"+id, nil)
			r.AddCookie(cookie)
			download := httptest.NewRecorder()
			router.ServeHTTP(download, r)
			if download.Code != 200 || !bytes.Equal(download.Body.Bytes(), data) || download.Header().Get("Content-Type") != fixture.mime || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatalf("changed Office download: %d type=%s", download.Code, download.Header().Get("Content-Type"))
			}
			for _, other := range corpus {
				if other.name == fixture.name {
					continue
				}
				t.Run("reject_as_"+other.name, func(t *testing.T) {
					w := uploadFileHTTP(t, router, cookie, c.ID, "mismatched"+filepath.Ext(other.name), data)
					if w.Code != 400 {
						t.Fatalf("%s accepted as %s: %d %s", fixture.name, other.name, w.Code, w.Body.String())
					}
				})
			}
			w = uploadFileHTTP(t, router, cookie, c.ID, "text"+filepath.Ext(fixture.name), []byte("This is text, not an Office binary"))
			if w.Code != 400 {
				t.Fatalf("text accepted as %s: %d", fixture.name, w.Code)
			}
			for _, bad := range [][]byte{data[:512], data[:len(data)/2]} {
				w = uploadFileHTTP(t, router, cookie, c.ID, fixture.name, bad)
				if w.Code != 400 {
					t.Fatalf("truncated %s accepted: %d", fixture.name, w.Code)
				}
			}
		})
	}
	if n := client.File.Query().CountX(ctx); n != 3 {
		t.Fatalf("published %d files, want only three valid originals", n)
	}
}

func TestFileLegacyOfficePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy Office browser").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	cookie := sessionCookie(t, ctx, sessions, admin)
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/file-legacy-office.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.File.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("browser batch created %d File identities, want 3", len(rows))
	}
	for _, f := range rows {
		if f.RemovedAt == nil || f.RemovedBy == nil || *f.RemovedBy != admin || f.UploadedBy != admin {
			t.Fatalf("browser lost removal/uploader metadata: %+v", f)
		}
	}
}
