package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

func TestFilePublicationReplayBelongsToOriginatingClientHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	authority := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}, {Permission: "file.download", Scope: authorization.All}}
	role, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Publication clients", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("publication-clients@test").SetPasswordHash("hash").SetName("Publisher").SetRole("tech").SaveX(ctx)
	if err := authority.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replay parent").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Replay Job").SaveX(ctx)
	files := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20)
	api := apiv1.NewRouter(pool, client, sessions, files)
	// Labels intentionally collide: only the authenticated session identifies a client.
	a, err := sessions.CreateMobile(ctx, u.ID, "Field tablet")
	if err != nil {
		t.Fatal(err)
	}
	b, err := sessions.CreateMobile(ctx, u.ID, "Field tablet")
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		text, ok := body.(string)
		if !ok {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			text = string(encoded)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(text))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	const name = "sensitive-publication.txt"
	const data = "exact publication bytes"
	publication := uuid.NewString()
	stage := func(token, filename, content string) string {
		t.Helper()
		var before, after int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='file_uploaded'`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewString()
		digest := sha256.Sum256([]byte(content))
		request(token, "POST", "/file-transfers", map[string]any{"id": id, "publication_id": publication, "object_type": "job", "object_id": j.ID, "name": filename, "size": len(content), "sha256": hex.EncodeToString(digest[:])}, 201)
		request(token, "PUT", "/file-transfers/"+id+"/chunks?offset=0", content, 200)
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='file_uploaded'`).Scan(&after); err != nil || before != after {
			t.Fatal("transfer staging created collaboration history", err)
		}
		return id
	}
	original := stage(a, name, data)
	accepted := request(a, "POST", "/file-transfers/"+original+"/publish", "", 200)
	var effect struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &effect); err != nil || effect.Data.ID == 0 {
		t.Fatalf("publication response: %s (%v)", accepted.Body, err)
	}
	// Rebuilding the production router cannot lose a persisted client binding.
	api = apiv1.NewRouter(pool, client, sessions, files)
	for _, id := range []string{original, stage(a, name, data)} {
		replayed := request(a, "POST", "/file-transfers/"+id+"/publish", "", 200)
		if replayed.Body.String() != accepted.Body.String() {
			t.Fatalf("same-client replay changed effect: %s", replayed.Body)
		}
	}
	var historyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='file_uploaded' AND object_type='job' AND object_id=$1`, j.ID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatal("publication/replay history must occur once", historyCount, err)
	}
	request(b, "GET", "/file-transfers/"+original, "", 404)
	other := stage(b, name, data)
	denied := request(b, "POST", "/file-transfers/"+other+"/publish", "", 409)
	if strings.Contains(denied.Body.String(), effect.Data.Revision) || strings.Contains(denied.Body.String(), `"data"`) {
		t.Fatalf("cross-client receipt leaked a representation: %s", denied.Body)
	}
	// An older receipt with unrecorded origin cannot be claimed by the first
	// replaying device, even when user, bytes and intent all match.
	originalPublication := publication
	publication = uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO file_publications(user_id,operation_id,intent,file_id) SELECT user_id,$2,intent,file_id FROM file_publications WHERE user_id=$1 AND operation_id=$3`, u.ID, publication, originalPublication); err != nil {
		t.Fatal(err)
	}
	unknown := stage(a, name, data)
	request(a, "POST", "/file-transfers/"+unknown+"/publish", "", 409)
	publication = originalPublication
	for _, id := range []string{stage(a, "changed.txt", data), stage(a, name, "changed publication bytes")} {
		request(a, "POST", "/file-transfers/"+id+"/publish", "", 409)
	}
	if got := request(a, "GET", fmt.Sprintf("/files/%d", effect.Data.ID), "", 200).Body.String(); got != data {
		t.Fatalf("publication bytes changed: %q", got)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replays created %d Files: %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='file' AND action='file.upload'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replays reattributed effects: %d %v", count, err)
	}
	// Success evidence cannot become cached authority, even for its original client.
	_, err = authority.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "View only after publication", []authorization.Grant{grants[0], grants[2]}, 1)
	if err != nil {
		t.Fatal(err)
	}
	request(a, "POST", "/file-transfers/"+original+"/publish", "", 403)
	_, err = authority.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Access removed", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/file-transfers/" + original + "/publish", fmt.Sprintf("/files/%d/metadata", effect.Data.ID)} {
		method := "POST"
		if strings.HasSuffix(path, "metadata") {
			method = "GET"
		}
		denied := request(a, method, path, "", 404)
		if strings.Contains(denied.Body.String(), name) || strings.Contains(denied.Body.String(), effect.Data.Revision) || strings.Contains(denied.Body.String(), `"data"`) {
			t.Fatalf("scope loss disclosed sensitive receipt: %s", denied.Body)
		}
	}
	_, err = authority.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Current authority restored", grants, 3)
	if err != nil {
		t.Fatal(err)
	}
	request(a, "POST", "/file-transfers/"+original+"/publish", "", 200)
	if err := sessions.RevokeMobile(ctx, a); err != nil {
		t.Fatal(err)
	}
	request(a, "POST", "/file-transfers/"+original+"/publish", "", 401)
	request(b, "POST", "/file-transfers/"+other+"/publish", "", 409)
}

func TestFilePublicationBrowserReceiptStaysNonsensitiveAfterScopeLossHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	authority := authorization.New(pool)
	role, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser publisher", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("browser-publication@test").SetPasswordHash("hash").SetName("Publisher").SetRole("tech").SaveX(ctx)
	if err := authority.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Receipt parent").SaveX(ctx)
	publication, action := uuid.NewString(), uuid.NewString()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	if err := m.WriteField("publication_id", publication); err != nil {
		t.Fatal(err)
	}
	f, err := m.CreateFormFile("file", "private-original-name.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("private file contents")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	r := httptest.NewRequest("POST", fmt.Sprintf("/files?object_type=customer&object_id=%d", c.ID), &body)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.Header.Set("X-FreeFSM-Action", action)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	fileID := w.Header().Get("File-ID")
	// A fresh browser session for this user keeps the existing web semantics.
	cookie = sessionCookie(t, ctx, sessions, u.ID)
	w = uploadFileHTTP(t, web, cookie, c.ID, "private-original-name.txt", []byte("private file contents"), publication)
	if w.Code != 200 || w.Header().Get("File-ID") != fileID {
		t.Fatalf("browser replay after sign-in: %d %s", w.Code, w.Body)
	}
	if _, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Original scope removed", nil, 1); err != nil {
		t.Fatal(err)
	}
	receipt := requestBody(t, web, cookie, "GET", "/save-actions/"+action, 200)
	if !strings.Contains(receipt, `"disposition":"saved"`) || !strings.Contains(receipt, `"target_id":`+fileID) {
		t.Fatalf("lost nonsensitive disposition/effect identity: %s", receipt)
	}
	for _, secret := range []string{"private-original-name.txt", "private file contents", "file_path", "client_key", "session_hash"} {
		if strings.Contains(receipt, secret) {
			t.Fatalf("receipt disclosed %q: %s", secret, receipt)
		}
	}
	expectStatus(t, web, cookie, "GET", "/files/"+fileID, 404)
}

func TestFilePublicationRacingClientsCannotShareOriginHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Racing clients").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("One publication").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20))
	publication := uuid.NewString()
	digest := sha256.Sum256([]byte("race bytes"))
	var tokens, transfers [2]string
	for i := range tokens {
		var err error
		tokens[i], err = sessions.CreateMobile(ctx, admin, "Same device label")
		if err != nil {
			t.Fatal(err)
		}
		transfers[i] = uuid.NewString()
		payload, err := json.Marshal(map[string]any{"id": transfers[i], "publication_id": publication, "object_type": "job", "object_id": j.ID, "name": "race.txt", "size": 10, "sha256": hex.EncodeToString(digest[:])})
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []struct {
			method, path, body string
			want               int
		}{{"POST", "/file-transfers", string(payload), 201}, {"PUT", "/file-transfers/" + transfers[i] + "/chunks?offset=0", "race bytes", 200}} {
			r := httptest.NewRequest(step.method, step.path, strings.NewReader(step.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+tokens[i])
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != step.want {
				t.Fatalf("stage: %d %s", w.Code, w.Body)
			}
		}
	}
	type result struct {
		client   int
		response *httptest.ResponseRecorder
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := range tokens {
		go func(i int) {
			<-start
			r := httptest.NewRequest("POST", "/file-transfers/"+transfers[i]+"/publish", nil)
			r.Header.Set("Authorization", "Bearer "+tokens[i])
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			results <- result{i, w}
		}(i)
	}
	close(start)
	winner := -1
	for range tokens {
		out := <-results
		switch out.response.Code {
		case 200:
			if winner != -1 {
				t.Fatal("both clients accepted the same publication")
			}
			winner = out.client
		case 409:
			if strings.Contains(out.response.Body.String(), `"data"`) {
				t.Fatal("loser received File representation")
			}
		default:
			t.Fatalf("publish: %d %s", out.response.Code, out.response.Body)
		}
	}
	if winner == -1 {
		t.Fatal("no publication accepted")
	}
	var bound bool
	hash := sha256.Sum256([]byte(tokens[winner]))
	if err := pool.QueryRow(ctx, `SELECT p.client_key='api:'||s.id::text AND (a.detail->>'session_id')::bigint=s.id FROM file_publications p JOIN sessions s ON s.user_id=p.user_id AND s.token_hash=$2 JOIN authorization_audit a ON a.target_type='file' AND a.target_id=p.file_id AND a.action='file.upload' WHERE p.operation_id=$1`, publication, hex.EncodeToString(hash[:])).Scan(&bound); err != nil || !bound {
		t.Fatalf("origin and audit did not commit together: %v", err)
	}
	if n := client.File.Query().CountX(ctx); n != 1 {
		t.Fatalf("created %d Files", n)
	}
}
