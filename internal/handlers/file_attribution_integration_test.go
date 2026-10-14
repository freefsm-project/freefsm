package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestFileAPIAuditAttributionHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Audit parent").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("File audit").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20))
	token, err := sessions.CreateMobile(ctx, admin, "File audit field tablet")
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256([]byte(token))
	var sessionID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE token_hash=$1`, hex.EncodeToString(tokenHash[:])).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, want int) string {
		t.Helper()
		var text string
		if raw, ok := body.(string); ok {
			text = raw
		} else {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			text = string(data)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(text))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		stageTestFileTransport(r, text)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	const transfer = "46000000-0000-4000-8000-000000000001"
	const publication = "46000000-0000-4000-8000-000000000002"
	digest := sha256.Sum256([]byte("audit bytes"))
	request("POST", "/file-transfers", map[string]any{"id": transfer, "publication_id": publication, "object_type": "job", "object_id": j.ID, "name": "audit.txt", "size": 11, "sha256": hex.EncodeToString(digest[:])}, 201)
	request("PUT", "/file-transfers/"+transfer+"/chunks?offset=0", "audit bytes", 200)
	published := request("POST", "/file-transfers/"+transfer+"/publish", "", 200)
	var effect struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(published), &effect); err != nil {
		t.Fatal(err)
	}
	assertAudit := func(action, operation string, occurred *time.Time) {
		t.Helper()
		var detail struct {
			OperationID      string    `json:"operation_id"`
			SessionID        int64     `json:"session_id"`
			DeviceName       string    `json:"device_name"`
			OccurredAt       time.Time `json:"occurred_at"`
			AcceptedAt       time.Time `json:"accepted_at"`
			OccurrenceSource string    `json:"occurrence_source"`
		}
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT detail FROM authorization_audit WHERE target_type='file' AND target_id=$1 AND action=$2 AND actor_id=$3 AND channel='api' AND role_id=1 AND scope='A' AND outcome='success'`, effect.Data.ID, action, admin).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.OperationID != operation || detail.SessionID != sessionID || detail.DeviceName != "File audit field tablet" || detail.AcceptedAt.IsZero() || detail.OccurredAt.IsZero() || detail.OccurredAt.After(detail.AcceptedAt) {
			t.Fatalf("incomplete API attribution: %s", raw)
		}
		if occurred != nil && (!detail.OccurredAt.Equal(*occurred) || detail.OccurrenceSource != "client") {
			t.Fatalf("lost occurrence: %s", raw)
		}
		if occurred == nil && (detail.OccurrenceSource != "server" || !detail.OccurredAt.Equal(detail.AcceptedAt)) {
			t.Fatalf("invented client occurrence: %s", raw)
		}
		if strings.Contains(string(raw), token) || strings.Contains(string(raw), hex.EncodeToString(tokenHash[:])) {
			t.Fatal("audit recorded bearer material")
		}
	}
	assertAudit("file.upload", publication, nil)
	request("POST", "/file-transfers/"+transfer+"/publish", "", 200)
	const rename = "46000000-0000-4000-8000-000000000003"
	const remove = "46000000-0000-4000-8000-000000000004"
	occurred := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	command := map[string]any{"operation_id": rename, "occurred_at": occurred, "name": "renamed.txt", "revision": effect.Data.Revision, "object_type": "job", "object_id": j.ID}
	command["operation_id"] = publication
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 409)
	command["operation_id"] = rename
	for _, invalidID := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		command["operation_id"] = invalidID
		request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 400)
	}
	command["operation_id"] = rename
	invalidOccurrenceOperation := newOperationKey()
	command["operation_id"] = invalidOccurrenceOperation
	command["occurred_at"] = time.Now().Add(time.Hour)
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 400)
	command["occurred_at"] = occurred
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 409)
	if receipt := request("GET", "/file-operations/"+invalidOccurrenceOperation, "", 200); !strings.Contains(receipt, `"code":"invalid_file"`) {
		t.Fatalf("terminal File rejection lost: %s", receipt)
	}
	command["operation_id"] = rename // explicitly reviewed new-ID repair
	command["device_name"] = "forged device"
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 400)
	delete(command, "device_name")
	// Attribution and effect must fail together; a retry keeps its operation ID.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_file_api_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='file' AND NEW.action='file.rename' THEN RAISE EXCEPTION 'injected File API audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_file_api_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_file_api_audit()`); err != nil {
		t.Fatal(err)
	}
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 500)
	unchanged := request("GET", fmt.Sprintf("/files/%d/metadata", effect.Data.ID), "", 200)
	if !strings.Contains(unchanged, `"name":"audit.txt"`) || !strings.Contains(unchanged, effect.Data.Revision) {
		t.Fatalf("audit failure changed File: %s", unchanged)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_file_api_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 200)
	assertAudit("file.rename", rename, &occurred)
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 200)
	firstDevice := token
	token, err = sessions.CreateMobile(ctx, admin, "Second File device")
	if err != nil {
		t.Fatal(err)
	}
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 409)
	token = firstDevice
	metadata := request("GET", fmt.Sprintf("/files/%d/metadata", effect.Data.ID), "", 200)
	if err := json.Unmarshal([]byte(metadata), &effect); err != nil {
		t.Fatal(err)
	}
	command["revision"] = effect.Data.Revision
	command["name"] = "changed-intent.txt"
	request("POST", fmt.Sprintf("/files/%d/rename", effect.Data.ID), command, 409)
	delete(command, "name")
	command["operation_id"] = remove
	command["revision"] = effect.Data.Revision
	request("POST", fmt.Sprintf("/files/%d/remove", effect.Data.ID), command, 200)
	assertAudit("file.remove", remove, &occurred)
	request("POST", fmt.Sprintf("/files/%d/remove", effect.Data.ID), command, 200)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='file' AND target_id=$1`, effect.Data.ID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("replays changed attribution: count=%d err=%v", count, err)
	}
	request(http.MethodGet, fmt.Sprintf("/files/%d", effect.Data.ID), "", 404)
	// Client occurrence belongs to publication intent, not its transfer attempt.
	const offlineTransfer = "46000000-0000-4000-8000-000000000005"
	const offlinePublication = "46000000-0000-4000-8000-000000000006"
	transferIntent := map[string]any{"id": offlineTransfer, "publication_id": offlinePublication, "object_type": "job", "object_id": j.ID, "name": "offline.txt", "size": 11, "sha256": hex.EncodeToString(digest[:]), "occurred_at": occurred}
	request("POST", "/file-transfers", transferIntent, 201)
	changedIntent := map[string]any{}
	for key, value := range transferIntent {
		changedIntent[key] = value
	}
	changedIntent["occurred_at"] = occurred.Add(time.Minute)
	request("POST", "/file-transfers", changedIntent, 409)
	request("PUT", "/file-transfers/"+offlineTransfer+"/chunks?offset=0", "audit bytes", 200)
	published = request("POST", "/file-transfers/"+offlineTransfer+"/publish", "", 200)
	if err := json.Unmarshal([]byte(published), &effect); err != nil {
		t.Fatal(err)
	}
	assertAudit("file.upload", offlinePublication, &occurred)
	if err := sessions.RevokeMobile(ctx, token); err != nil {
		t.Fatal(err)
	}
	assertAudit("file.upload", offlinePublication, &occurred)
}
