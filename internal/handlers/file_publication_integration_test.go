package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func uploadFileHTTP(t *testing.T, router http.Handler, cookie *http.Cookie, parent int64, name string, data []byte, operation ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	if len(operation) > 0 {
		if err := m.WriteField("publication_id", operation[0]); err != nil {
			t.Fatal(err)
		}
	}
	f, err := m.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", fmt.Sprintf("/files?object_type=customer&object_id=%d", parent), &body)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestFilePublicationRechecksGrantAtCommitHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	authority := authorization.New(pool)
	role, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Uploader before revocation", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("revoked-upload@test").SetPasswordHash("hash").SetName("Uploader").SetRole("tech").SaveX(ctx)
	if err = authority.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commit authority").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) boundary`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- uploadFileHTTP(t, router, cookie, c.ID, "revoked.txt", []byte("durable but unauthorized"))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publication never reached the commit lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='file.upload'`, role); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-result
	if w.Code != 403 {
		t.Fatalf("revoked grant committed: %d %s", w.Code, w.Body.String())
	}
	if n := client.File.Query().CountX(ctx); n != 0 {
		t.Fatalf("publication escaped revoked grant: %d", n)
	}
}

func TestFileFormatValidationThroughHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Formats").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngBytes, jpegBytes, gifBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	archive := func(entries ...string) []byte {
		var out bytes.Buffer
		z := zip.NewWriter(&out)
		for i := 0; i < len(entries); i += 2 {
			f, err := z.CreateHeader(&zip.FileHeader{Name: entries[i], Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write([]byte(entries[i+1])); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		data   []byte
		status int
	}{
		{"image.png", pngBytes.Bytes(), 200}, {"image.jpg", jpegBytes.Bytes(), 200}, {"image.gif", gifBytes.Bytes(), 200},
		{"image.webp", webp, 200},
		{"document.odt", archive("mimetype", "application/vnd.oasis.opendocument.text", "content.xml", `<?xml version="1.0"?><document/>`), 200},
		{"sheet.ods", archive("mimetype", "application/vnd.oasis.opendocument.spreadsheet", "content.xml", `<?xml version="1.0"?><document/>`), 200},
		{"sheet.xlsx", archive("[Content_Types].xml", `<?xml version="1.0"?><Types/>`, "xl/workbook.xml", `<?xml version="1.0"?><workbook/>`), 200},
		{"slides.pptx", archive("[Content_Types].xml", `<?xml version="1.0"?><Types/>`, "ppt/presentation.xml", `<?xml version="1.0"?><presentation/>`), 200},
		{"data.json", []byte(`{"valid":true}`), 200}, {"broken.json", []byte(`{"valid":`), 400}, {"data.csv", []byte("name,value\nalpha,1\n"), 200},
		{"archive.zip", archive("../not-extracted.txt", "payload"), 200}, {"broken.zip", []byte("PK\x03\x04broken"), 400},
		{"document.docx", archive("[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`, "word/document.xml", `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`), 200},
		{"forged.jpg", pngBytes.Bytes(), 400}, {"program.exe", []byte("MZ executable"), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := uploadFileHTTP(t, router, cookie, c.ID, tc.name, tc.data)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestFileStorageReviewReportsLegacyAndMissingBytesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Storage review").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if w := uploadFileHTTP(t, router, cookie, c.ID, "legacy.txt", []byte("hello")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f := client.File.Query().OnlyX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE files SET sha256=NULL WHERE id=$1`, f.ID); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/files/storage-review", 200)
	if !strings.Contains(body, "legacy_digest_unrecorded") {
		t.Fatalf("legacy discrepancy hidden: %s", body)
	}
	if err := os.Remove(f.FilePath); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/files/%d", f.ID), 503)
	body = requestBody(t, router, cookie, "GET", "/files/storage-review", 200)
	if !strings.Contains(body, "durable_bytes_missing") {
		t.Fatalf("missing durable bytes hidden: %s", body)
	}
	retained := client.File.GetX(ctx, f.ID)
	if retained.OriginalName != "legacy.txt" || retained.Sha256 != nil || retained.UploadedBy != admin {
		t.Fatal("review invented or rewrote metadata")
	}
}

type gatedFileBody struct {
	io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (b *gatedFileBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(p)
}

func TestFilePublicationRechecksParentAfterTransferHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Parent lost during transfer").SaveX(ctx)
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	part, err := m.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	gate := &gatedFileBody{Reader: &body, entered: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("POST", fmt.Sprintf("/files?object_type=customer&object_id=%d", c.ID), gate)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.AddCookie(sessionCookie(t, ctx, sessions, admin))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { router.ServeHTTP(w, r); close(done) }()
	<-gate.entered
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
	close(gate.release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if w.Code < 400 {
		t.Fatalf("published after parent archive: %d", w.Code)
	}
	if n := client.File.Query().CountX(ctx); n != 0 {
		t.Fatalf("published %d files after parent loss", n)
	}
}

func TestConcurrentFileRenameHasOneWinnerHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent rename").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if w := uploadFileHTTP(t, router, cookie, c.ID, "note.txt", []byte("hello")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f := client.File.Query().OnlyX(ctx)
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, name := range []string{"one.txt", "two.txt"} {
		go func(name string) {
			<-start
			r := httptest.NewRequest("POST", fmt.Sprintf("/files/%d/rename", f.ID), strings.NewReader(url.Values{"original_name": {name}, "revision": {"1"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w.Code
		}(name)
	}
	close(start)
	a, b := <-results, <-results
	if !(a == 200 && b == 409 || a == 409 && b == 200) {
		t.Fatalf("concurrent results %d %d", a, b)
	}
}

func TestFileResumableAPIPublishesOnlyAfterDigestVerification(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API files").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field work").SaveX(ctx)
	files := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20)
	api := apiv1.NewRouter(pool, client, sessions, files)
	token, err := sessions.CreateMobile(ctx, admin, "file test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	sum := sha256.Sum256([]byte("hello world"))
	const transfer = "d90eb9ac-73b0-4744-95b9-2418c3b60046"
	init, _ := json.Marshal(map[string]any{"id": transfer, "publication_id": "49304b16-11b4-404d-9951-94d884e80446", "object_type": "job", "object_id": j.ID, "name": "field.txt", "size": 11, "sha256": hex.EncodeToString(sum[:])})
	request("POST", "/file-transfers", string(init), 201)
	request("PUT", "/file-transfers/"+transfer+"/chunks?offset=0", "hello ", 200)
	request("PUT", "/file-transfers/"+transfer+"/chunks?offset=0", "hello ", 200)
	request("POST", "/file-transfers/"+transfer+"/publish", "", 409)
	if n := client.File.Query().CountX(ctx); n != 0 {
		t.Fatalf("transfer published early: %d", n)
	}
	request("PUT", "/file-transfers/"+transfer+"/chunks?offset=6", "world", 200)
	result := request("POST", "/file-transfers/"+transfer+"/publish", "", 200)
	request("POST", "/file-transfers/"+transfer+"/publish", "", 200)
	var published struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result), &published); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", fmt.Sprintf("/files/%d", published.Data.ID), "", 200); got != "hello world" {
		t.Fatalf("exact bytes: %q", got)
	}
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("retry duplicated: %d", n)
	}
	originalRevision := published.Data.Revision
	request("POST", "/files/"+fmt.Sprint(published.Data.ID)+"/rename", fmt.Sprintf(`{"name":"renamed.txt","revision":%q,"object_type":"job","object_id":%d}`, originalRevision, j.ID), 200)
	request("POST", "/files/"+fmt.Sprint(published.Data.ID)+"/rename", fmt.Sprintf(`{"name":"stale.txt","revision":%q,"object_type":"job","object_id":%d}`, originalRevision, j.ID), 409)
	metadata := request("GET", fmt.Sprintf("/files/%d/metadata", published.Data.ID), "", 200)
	if err := json.Unmarshal([]byte(metadata), &published); err != nil {
		t.Fatal(err)
	}
	request("POST", "/files/"+fmt.Sprint(published.Data.ID)+"/remove", fmt.Sprintf(`{"revision":%q,"object_type":"job","object_id":%d}`, published.Data.Revision, j.ID), 200)
	request("POST", "/files/"+fmt.Sprint(published.Data.ID)+"/remove", fmt.Sprintf(`{"revision":%q,"object_type":"job","object_id":%d}`, originalRevision, j.ID), 200)
	request("GET", fmt.Sprintf("/files/%d", published.Data.ID), "", 404)
	const badTransfer = "d90eb9ac-73b0-4744-95b9-2418c3b60047"
	bad := strings.Replace(string(init), transfer, badTransfer, 1)
	request("POST", "/file-transfers", bad, 201)
	request("PUT", "/file-transfers/"+badTransfer+"/chunks?offset=0", "HELLO WORLD", 200)
	request("POST", "/file-transfers/"+badTransfer+"/publish", "", 400)
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("digest mismatch published: %d", n)
	}
	otherToken, err := sessions.CreateMobile(ctx, admin, "different device")
	if err != nil {
		t.Fatal(err)
	}
	token = otherToken
	request("GET", "/file-transfers/"+transfer, "", 404)
}

func TestFilePublicationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser files").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	server := httptest.NewServer(router)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/file-publication.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("browser retry created %d identities", n)
	}
}

func TestProjectBasicViewOffersFilesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Project customer").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Project files").SaveX(ctx)
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/projects/%d", p.ID), 200)
	if !strings.Contains(body, "files-drop-form") {
		t.Fatal("Project basic view lacks File collection")
	}
}

func TestFilePublicationRetryHasOneIdentityHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retry files").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for range 2 {
		w := uploadFileHTTP(t, router, cookie, c.ID, "note.txt", []byte("hello"), "49304b16-11b4-404d-9951-94d884e80446")
		if w.Code != 200 {
			t.Fatalf("upload: %d %s", w.Code, w.Body.String())
		}
	}
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("retry published %d files", n)
	}
	w := uploadFileHTTP(t, router, cookie, c.ID, "changed.txt", []byte("hello"), "49304b16-11b4-404d-9951-94d884e80446")
	if w.Code != 409 {
		t.Fatalf("changed intent: %d %s", w.Code, w.Body.String())
	}
}

func TestFileRenamePreservesExtensionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Rename files").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := uploadFileHTTP(t, router, cookie, c.ID, "note.txt", []byte("hello"))
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	f := client.File.Query().OnlyX(ctx)
	r := httptest.NewRequest("POST", fmt.Sprintf("/files/%d/rename", f.ID), strings.NewReader(url.Values{"original_name": {"renamed.png"}, "revision": {"1"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("changed extension: %d %s", w.Code, w.Body.String())
	}
}

func TestFileRemovalRetainsMetadataAndBytesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained files").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := uploadFileHTTP(t, router, cookie, c.ID, "note.txt", []byte("hello"))
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	f := client.File.Query().OnlyX(ctx)
	r := httptest.NewRequest("POST", fmt.Sprintf("/files/%d/delete", f.ID), strings.NewReader("revision=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if _, err := client.File.Get(ctx, f.ID); err != nil {
		t.Fatalf("removal lost recorded identity: %v", err)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/files/%d", f.ID), 404)
}

func TestFileStaleRenameCannotOverwriteHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent files").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := uploadFileHTTP(t, router, cookie, c.ID, "note.txt", []byte("hello"))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f := client.File.Query().OnlyX(ctx)
	for _, tc := range []struct {
		name, revision string
		status         int
	}{{"first.txt", "1", 200}, {"stale.txt", "1", 409}, {"first.txt", "1", 200}, {"note.txt", "2", 200}, {"aba.txt", "1", 409}} {
		r := httptest.NewRequest("POST", fmt.Sprintf("/files/%d/rename", f.ID), strings.NewReader(url.Values{"original_name": {tc.name}, "revision": {tc.revision}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("rename %s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestFileUploadRequiresIndependentGrantHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	a := authorization.New(pool)
	role, err := a.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "View only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("file-view@test").SetPasswordHash("hash").SetName("Viewer").SetRole("dispatcher").SaveX(ctx)
	if err := a.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent grant").SaveX(ctx)
	w := uploadFileHTTP(t, router, sessionCookie(t, ctx, sessions, u.ID), c.ID, "note.txt", []byte("hello"))
	if w.Code != 403 {
		t.Fatalf("View without Upload: %d %s", w.Code, w.Body.String())
	}
	_, err = a.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed uploader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	w = uploadFileHTTP(t, router, cookie, c.ID, "allowed.txt", []byte("hello"))
	if w.Code != 200 {
		t.Fatalf("explicit grant after rename: %d %s", w.Code, w.Body.String())
	}
	fileID := w.Header().Get("File-ID")
	expectStatus(t, router, cookie, "GET", "/files/"+fileID, 403)
	_, err = a.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Own manager", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}, {Permission: "file.rename", Scope: authorization.Own}, {Permission: "file.remove", Scope: authorization.Own}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	post := func(id, name string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/files/"+id+"/rename", strings.NewReader(url.Values{"original_name": {name}, "revision": {"1"}}.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("own rename status=%d want=%d: %s", w.Code, want, w.Body.String())
		}
	}
	post(fileID, "own.txt", 200)
	other := uploadFileHTTP(t, router, sessionCookie(t, ctx, sessions, admin), c.ID, "other.txt", []byte("hello"))
	if other.Code != 200 {
		t.Fatal(other.Body.String())
	}
	post(other.Header().Get("File-ID"), "not-own.txt", 403)
}

func TestFilePublicationAuditFailureRollsBackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic files").SaveX(ctx)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_file_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='file' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_file_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_file_audit()`); err != nil {
		t.Fatal(err)
	}
	w := uploadFileHTTP(t, router, sessionCookie(t, ctx, sessions, admin), c.ID, "note.txt", []byte("hello"))
	if w.Code < 500 {
		t.Fatalf("audit failure falsely succeeded: %d %s", w.Code, w.Body.String())
	}
	if n := client.File.Query().CountX(ctx); n != 0 {
		t.Fatalf("published %d files despite audit failure", n)
	}
}

func TestFilePublicationRejectsContentExtensionMismatchHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("File validation").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	w := uploadFileHTTP(t, router, cookie, c.ID, "notes.txt", []byte("ordinary text"))
	if w.Code != 200 {
		t.Fatalf("valid text: %d %s", w.Code, w.Body.String())
	}
	w = uploadFileHTTP(t, router, cookie, c.ID, "forged.png", []byte("ordinary text"))
	if w.Code != 400 {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body.String())
	}
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("published %d files; want only valid text", n)
	}
}
