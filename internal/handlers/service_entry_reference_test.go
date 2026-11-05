package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestServiceEntryProtectedReference(t *testing.T) {
	for _, drift := range []string{"stale", "ABA"} {
		t.Run(drift, func(t *testing.T) { serviceEntryProtectedReference(t, drift) })
	}
}

func serviceEntryProtectedReference(t *testing.T, drift string) {
	t.Helper()
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
		if r.URL.Path == "/__fixture/restore" {
			_, err := pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			fresh, err := sessions.CreateMobile(ctx, admin, "Restored service reference")
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
			return
		}
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
	input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "user": admin, "date": time.Now().UTC().Format(time.DateOnly), "drift": drift})
	historyCheck(t, err)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/service-entry-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("protected service reference: %s", out)
	historyCheck(t, err)
}

func TestServiceEntryRelationshipLossProtectedReference(t *testing.T) {
	for _, loss := range []string{"unlink", "transfer", "archive"} {
		for _, failWrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persistence_failure_%t", loss, failWrite), func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				auth := authorization.New(pool)
				actor := authorization.Actor{UserID: admin}
				role, err := auth.SaveRole(ctx, actor, 0, "Service-only field worker", []authorization.Grant{
					{Permission: "job.view", Scope: authorization.Assigned},
					{Permission: "asset.view", Scope: authorization.Context},
					{Permission: "service_entry.view", Scope: authorization.Assigned},
					{Permission: "service_entry.create", Scope: authorization.Assigned},
					{Permission: "service_entry.revise", Scope: authorization.Assigned},
				})
				historyCheck(t, err)
				u := client.User.Create().SetCompanyID(company).SetEmail("relationship-reference@test").SetName("Field worker").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
				historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original Customer").SaveX(ctx)
				next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Next Customer").SaveX(ctx)
				typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
				a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Private pump").SetAssetTypeID(typ.ID).SaveX(ctx)
				other := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Other pump").SetAssetTypeID(typ.ID).SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Affected Job").SaveX(ctx)
				otherJob := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(other.ID).SetJobType("Other Job").SaveX(ctx)
				for _, job := range []int64{j.ID, otherJob.ID} {
					client.JobAssignment.Create().SetJobID(job).SetUserID(u.ID).SaveX(ctx)
				}
				flow := statusflow.New(pool)
				completed, err := flow.Default(ctx, company, statusflow.Job, statusflow.JobCompleted)
				historyCheck(t, err)
				historyCheck(t, flow.TransitionJob(ctx, statusflow.Actor{ID: admin, CompanyID: company}, j.ID, completed.ID))
				token, err := sessions.CreateMobile(ctx, u.ID, "Relationship loss reference")
				historyCheck(t, err)
				cookie := sessionCookie(t, ctx, sessions, admin)
				api := apiv1.NewRouter(pool, client, sessions)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/__fixture/relationship-loss" {
						var err error
						switch loss {
						case "archive":
							err = auth.AssetLifecycle(ctx, actor, a.ID, false)
						case "transfer":
							err = auth.ChangeAssetAssociation(ctx, actor, a.ID, authorization.AssetAssociationInput{Action: "transfer", CustomerID: next.ID, EffectiveDate: time.Now().UTC().Format(time.DateOnly), Reason: "Reference departure", Revision: 0})
						case "unlink":
							var link int64
							err = pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1 AND asset_id=$2 AND operational AND unlinked_at IS NULL`, j.ID, a.ID).Scan(&link)
							if err == nil {
								fieldPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
							}
						}
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
				input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "user": u.ID, "date": time.Now().UTC().Format(time.DateOnly), "fail_write": failWrite})
				historyCheck(t, err)
				runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/service-entry-relationship-http.cjs")
				cmd.Stdin = bytes.NewReader(input)
				out, err := cmd.CombinedOutput()
				t.Logf("protected relationship reference: %s", out)
				historyCheck(t, err)
			})
		}
	}
}
