package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestJobFieldsExecutableReferenceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, name := range []string{"First", "Second"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {name}, "field_type": {"text"}}, 303)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Executable field reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "node", "testdata/browser/job-fields-reference.cjs", server.URL, token, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("reference: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.fields.edit' AND target_id=$1`, j.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 4 {
		t.Fatalf("expected four accepted value changes, not replay/rejection audit: %d", events)
	}
	for _, statement := range []string{`UPDATE job_field_operations SET outcome='accepted'`, `DELETE FROM job_field_operations`, `UPDATE authorization_audit SET detail='{}' WHERE action='job.fields.edit'`} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("immutable evidence accepted: %s", statement)
		}
	}
}
