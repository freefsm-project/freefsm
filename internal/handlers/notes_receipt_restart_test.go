package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotesReceiptRetentionAndRestartHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained Notes").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Retained receipt client")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	// Controlled acceptance clock fixture; these are not asserted to be actual
	// installed historical facts. Immutable rows are never updated or deleted.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION old_notes_receipt_clock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.accepted_at=clock_timestamp()-interval '31 days'; RETURN NEW; END $$; CREATE TRIGGER old_notes_receipt_clock BEFORE INSERT ON job_execution_receipts FOR EACH ROW EXECUTE FUNCTION old_notes_receipt_clock(); CREATE FUNCTION old_notes_admission_clock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.admitted_at=clock_timestamp()-interval '31 days'; RETURN NEW; END $$; CREATE TRIGGER old_notes_admission_clock BEFORE INSERT ON operation_admissions FOR EACH ROW EXECUTE FUNCTION old_notes_admission_clock()`); err != nil {
		t.Fatal(err)
	}
	intent := executionNotes(t, api, token, path, "Retained first effect")
	receipt := executionAPI(t, api, token, "POST", path+"/field-notes", intent, 200)
	var parsed struct {
		Data struct {
			Accepted time.Time `json:"accepted_at"`
		}
	}
	if err := json.Unmarshal([]byte(receipt), &parsed); err != nil {
		t.Fatal(err)
	}
	if time.Since(parsed.Data.Accepted) < 30*24*time.Hour {
		t.Fatal("fixture did not cross retention floor")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER old_notes_receipt_clock ON job_execution_receipts; DROP TRIGGER old_notes_admission_clock ON operation_admissions`); err != nil {
		t.Fatal(err)
	}
	executionAPI(t, api, token, "POST", path+"/field-notes", executionNotes(t, api, token, path, "Newer effect survives replay"), 200)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(intent)
	command := exec.Command(binary, "-test.run=^TestNotesReceiptRestartProbe$", "-test.v")
	command.Env = append(os.Environ(), "FREEFSM_NOTES_PROBE_DSN="+pool.Config().ConnString(), "FREEFSM_NOTES_PROBE_TOKEN="+token, "FREEFSM_NOTES_PROBE_PATH="+path, "FREEFSM_NOTES_PROBE_INTENT="+string(raw), "FREEFSM_NOTES_PROBE_RECEIPT="+receipt)
	out, err := command.CombinedOutput()
	t.Logf("fresh process: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-entry-point reuse must never turn a retained Notes ID into Status.
	status := executionIntent(t, api, token, path, "job:pending")
	status["operation_id"] = intent["operation_id"]
	executionAPI(t, api, token, "POST", path+"/status", status, 409)
	status["operation_id"] = uuid.NewString()
	executionAPI(t, api, token, "POST", path+"/status", status, 200)
}

func TestNotesReceiptRestartProbe(t *testing.T) {
	dsn := os.Getenv("FREEFSM_NOTES_PROBE_DSN")
	if dsn == "" {
		t.Skip("subprocess helper invoked by retention/restart parent")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, raw)))
	defer client.Close()
	api := apiv1.NewRouter(pool, client, services.NewSessionService(pool))
	token, path := os.Getenv("FREEFSM_NOTES_PROBE_TOKEN"), os.Getenv("FREEFSM_NOTES_PROBE_PATH")
	var intent map[string]any
	if err := json.Unmarshal([]byte(os.Getenv("FREEFSM_NOTES_PROBE_INTENT")), &intent); err != nil {
		t.Fatal(err)
	}
	want := os.Getenv("FREEFSM_NOTES_PROBE_RECEIPT")
	for _, got := range []string{executionAPI(t, api, token, "GET", "/job-execution-operations/"+intent["operation_id"].(string), nil, 200), executionAPI(t, api, token, "POST", path+"/field-notes", intent, 200)} {
		if got != want {
			t.Fatal("restart or retention changed immutable disposition")
		}
	}
	body := executionAPI(t, api, token, "GET", path+"/field-notes", nil, 200)
	assertContains(t, body, "Newer effect survives replay")
	history := executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 200)
	var revisions struct{ Data []struct{ Content string } }
	if err := json.Unmarshal([]byte(history), &revisions); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range revisions.Data {
		if r.Content == "Retained first effect" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate effect after restart: %d", count)
	}
}
