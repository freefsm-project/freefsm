package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

// Each contender is a real authenticated mutation, with an independent valid
// target and payload. A losing family must never create a second effect.
func TestOperationCompositionCrossFamilyHTTP(t *testing.T) {
	for _, family := range []string{"comment", "clock", "transfer", "commercial"} {
		for _, order := range []string{"notes_first", "other_first", "concurrent"} {
			t.Run(family+"/"+order, func(t *testing.T) {
				pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Binding owner").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Binding target").SaveX(ctx)
				token, err := sessions.CreateMobile(ctx, admin, "Composition")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20))
				path := fmt.Sprintf("/jobs/%d", j.ID)
				notes := executionNotes(t, api, token, path, "Exactly once notes")
				op := uuid.NewString()
				notes["operation_id"] = op
				otherPath := path + "/comments"
				other := map[string]any{"operation_id": op, "content": "Exactly once comment"}
				if family == "clock" {
					otherPath = "/time-entries/clock-in"
					other = map[string]any{"operation_id": op}
				}
				otherSuccess := 200
				if family == "transfer" {
					sum := sha256.Sum256([]byte("bytes"))
					otherPath = "/file-transfers"
					other = map[string]any{"id": uuid.NewString(), "publication_id": uuid.NewString(), "object_type": "job", "object_id": j.ID, "name": "binding.txt", "size": 5, "sha256": hex.EncodeToString(sum[:])}
					otherSuccess = 201
				}
				if family == "commercial" {
					otherPath = "/estimates"
					other = map[string]any{"operation_id": op, "client_id": uuid.NewString(), "job_id": j.ID, "title": "Exactly once document"}
				}
				call := func(path string, body any) *httptest.ResponseRecorder {
					raw, _ := json.Marshal(body)
					r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Operation-ID", op)
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					return w
				}
				var a, b *httptest.ResponseRecorder
				switch order {
				case "notes_first":
					a = call(path+"/field-notes", notes)
					b = call(otherPath, other)
				case "other_first":
					b = call(otherPath, other)
					a = call(path+"/field-notes", notes)
				default:
					var wg sync.WaitGroup
					wg.Add(2)
					go func() { defer wg.Done(); a = call(path+"/field-notes", notes) }()
					go func() { defer wg.Done(); b = call(otherPath, other) }()
					wg.Wait()
				}
				if !((a.Code == 200 && b.Code == 409) || (a.Code == 409 && b.Code == otherSuccess)) {
					t.Fatalf("expected one effect and one identity conflict: notes=%d %s other=%d %s", a.Code, a.Body, b.Code, b.Body)
				}
				var effects int
				if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM job_notes_history WHERE job_id=$1 AND NOT baseline)+(SELECT count(*) FROM comments WHERE object_type='job' AND object_id=$1)+(SELECT count(*) FROM time_entries WHERE user_id=$2)+(SELECT count(*) FROM file_transfers WHERE user_id=$2)+(SELECT count(*) FROM estimates WHERE job_id=$1)`, j.ID, admin).Scan(&effects); err != nil {
					t.Fatal(err)
				}
				if effects != 1 {
					t.Fatalf("effects=%d", effects)
				}
				if a.Code == 200 {
					if replay := call(path+"/field-notes", notes); replay.Code != 200 {
						t.Fatalf("original replay: %d %s", replay.Code, replay.Body)
					}
				} else {
					if replay := call(otherPath, other); replay.Code != otherSuccess {
						t.Fatalf("original replay: %d %s", replay.Code, replay.Body)
					}
				}
			})
		}
	}
}

func TestOperationCompositionTransferReceiptHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Transport owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Transport target").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Transport binding")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20))
	call := func(method, path, op, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Operation-ID", op)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.String()
	}
	id, op := uuid.NewString(), uuid.NewString()
	digest := sha256.Sum256([]byte("hello"))
	intent := map[string]any{"id": id, "publication_id": uuid.NewString(), "object_type": "job", "object_id": j.ID, "name": "bound.txt", "size": 5, "sha256": hex.EncodeToString(digest[:])}
	raw, _ := json.Marshal(intent)
	call("POST", "/file-transfers", op, string(raw), 201)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	notes := executionNotes(t, api, token, path, "Must not write")
	notes["operation_id"] = op
	executionAPI(t, api, token, "POST", path+"/field-notes", notes, 409)
	chunk := uuid.NewString()
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", chunk, "hello", 200)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", chunk, "HELLO", 409)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{op, chunk} {
		receipt := call("GET", "/file-transfer-operations/"+key, "", "", 200)
		if !strings.Contains(receipt, `"code":"accepted"`) || !strings.Contains(receipt, id) {
			t.Fatalf("safe receipt: %s", receipt)
		}
		for _, secret := range []string{"bound.txt", "sha256", "object_id", "object_type", "hello"} {
			if strings.Contains(receipt, secret) {
				t.Fatalf("receipt leaks %s: %s", secret, receipt)
			}
		}
	}
}
