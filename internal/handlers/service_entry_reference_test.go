package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestServiceEntryProtectedReference(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected service Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Protected service Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Encrypted Service Entry reference")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/revoke" {
			_, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	historyCheck(t, os.Chmod(directory, 0700))
	input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "user": admin, "date": time.Now().UTC().Format(time.DateOnly)})
	historyCheck(t, err)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/service-entry-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("protected service reference: %s", out)
	historyCheck(t, err)
}
