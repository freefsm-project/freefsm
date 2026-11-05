package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func serviceAPIRequest(api http.Handler, token, method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestServiceEntryAPICommitFenceAndLostResponseScopeLoss(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}, {Permission: "service_entry.create", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, actor, 0, "Service API worker", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("service-api@test").SetName("API worker").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(asset.ID).SetJobType("API service").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(u.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Service device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", job.ID, asset.ID)
	get := serviceAPIRequest(api, token, "GET", path, nil)
	if get.Code != 200 {
		t.Fatalf("read=%d %s", get.Code, get.Body.String())
	}
	var collection struct {
		Data struct {
			LinkID  int64                        `json:"link_id"`
			Entries []authorization.ServiceEntry `json:"entries"`
		} `json:"data"`
	}
	if err = json.Unmarshal(get.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	command := map[string]any{"operation_id": newOperationKey(), "link_id": collection.Data.LinkID, "service_date": time.Now().UTC().Format("2006-01-02"), "summary": "Private service summary", "responsible_user_ids": []int64{u.ID}}
	for _, invalid := range []struct {
		key   string
		value any
	}{{"service_date", "2026-02-30"}, {"service_date", "0000-01-01"}, {"summary", " \t\n"}, {"summary", "Invalid\x00text"}, {"responsible_user_ids", []int64{}}, {"responsible_user_ids", []int64{-1}}} {
		bad := map[string]any{}
		for key, value := range command {
			bad[key] = value
		}
		bad[invalid.key] = invalid.value
		bad["operation_id"] = newOperationKey()
		response := serviceAPIRequest(api, token, "POST", path, bad)
		if response.Code != 409 {
			t.Fatalf("invalid %s: %d %s", invalid.key, response.Code, response.Body.String())
		}
	}
	response := serviceAPIRequest(api, token, "POST", path, command)
	if response.Code != 200 {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	accepted := response.Body.String()
	// Revise is independent from Create. Possession of the entry cannot grant it.
	get = serviceAPIRequest(api, token, "GET", path, nil)
	if err = json.Unmarshal(get.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	revision := map[string]any{}
	for key, value := range command {
		revision[key] = value
	}
	revision["operation_id"] = newOperationKey()
	revision["revision"] = collection.Data.Entries[0].Revision
	response = serviceAPIRequest(api, token, "POST", fmt.Sprintf("%s/%d", path, collection.Data.Entries[0].ID), revision)
	if response.Code != 403 {
		t.Fatalf("independent revise=%d %s", response.Code, response.Body.String())
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	pending := map[string]any{}
	for key, value := range command {
		pending[key] = value
	}
	pending["operation_id"] = newOperationKey()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serviceAPIRequest(api, token, "POST", path, pending) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request never reached authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response = <-done
	if response.Code != 404 {
		t.Fatalf("scope loss waiter=%d %s", response.Code, response.Body.String())
	}
	receipt := serviceAPIRequest(api, token, "GET", "/service-entry-operations/"+command["operation_id"].(string), nil)
	if receipt.Code != 200 || receipt.Body.String() != accepted || strings.Contains(receipt.Body.String(), "Private service") {
		t.Fatalf("lost-response receipt=%d %s", receipt.Code, receipt.Body.String())
	}
	response = serviceAPIRequest(api, token, "POST", path, command)
	if response.Code != 200 || response.Body.String() != accepted {
		t.Fatalf("accepted replay after scope loss=%d %s", response.Code, response.Body.String())
	}
	if response = serviceAPIRequest(api, token, "GET", path, nil); response.Code != 404 {
		t.Fatalf("scope-lost history=%d", response.Code)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("effects=%d err=%v", count, err)
	}
}

func TestServiceEntryLifecycleRacesRetainAttributableOutcome(t *testing.T) {
	for _, lifecycle := range []string{"archive", "transfer", "unlink"} {
		for _, mutation := range []string{"create", "revise"} {
			t.Run(lifecycle+"/"+mutation, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive Customer").SaveX(ctx)
				next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Next Customer").SaveX(ctx)
				typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
				asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
				job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(asset.ID).SetJobType("Completed service").SaveX(ctx)
				// The field collection requires assignment even for All Records
				// administrators; retain the lifecycle/write race assertions below.
				client.JobAssignment.Create().SetJobID(job.ID).SetUserID(admin).SaveX(ctx)
				// Completion with zero entries remains valid and does not manufacture one.
				flow := statusflow.New(pool)
				completed, err := flow.Default(ctx, company, statusflow.Job, statusflow.JobCompleted)
				if err != nil {
					t.Fatal(err)
				}
				if err := flow.TransitionJob(ctx, statusflow.Actor{ID: admin, CompanyID: company}, job.ID, completed.ID); err != nil {
					t.Fatal(err)
				}
				token, err := sessions.CreateMobile(ctx, admin, "Archive device")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions)
				path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", job.ID, asset.ID)
				var collection struct {
					Data struct {
						LinkID  int64                        `json:"link_id"`
						Entries []authorization.ServiceEntry `json:"entries"`
					} `json:"data"`
				}
				response := serviceAPIRequest(api, token, "GET", path, nil)
				if response.Code != 200 {
					t.Fatalf("read=%d", response.Code)
				}
				if err = json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
					t.Fatal(err)
				}
				if len(collection.Data.Entries) != 0 {
					t.Fatal("completion invented service")
				}
				command := map[string]any{"operation_id": newOperationKey(), "link_id": collection.Data.LinkID, "service_date": time.Now().UTC().Format("2006-01-02"), "summary": "Before archive", "responsible_user_ids": []int64{admin}}
				response = serviceAPIRequest(api, token, "POST", path, command)
				if response.Code != 200 {
					t.Fatalf("create=%d %s", response.Code, response.Body.String())
				}
				response = serviceAPIRequest(api, token, "GET", path, nil)
				if err = json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
					t.Fatal(err)
				}
				entry := collection.Data.Entries[0]
				command["operation_id"] = newOperationKey()
				command["revision"] = entry.Revision
				command["summary"] = "Racing revision"
				target := fmt.Sprintf("%s/%d", path, entry.ID)
				if mutation == "create" {
					target = path
					delete(command, "revision")
				}
				start := make(chan struct{})
				cookie := sessionCookie(t, ctx, sessions, admin)
				unlinkPath := fmt.Sprintf("/jobs/%d/assets", job.ID)
				unlinkValues := url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(collection.Data.LinkID)}}
				var unlinkRequest *http.Request
				if lifecycle == "unlink" {
					values := currentJobFormValues(t, router, cookie, unlinkPath, unlinkValues)
					unlinkRequest = httptest.NewRequest("POST", unlinkPath, strings.NewReader(values.Encode()))
					unlinkRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					unlinkRequest.AddCookie(cookie)
				}
				unlinkConflict := false
				archive := make(chan error, 1)
				revise := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					<-start
					auth := authorization.New(pool)
					if lifecycle == "archive" {
						archive <- auth.AssetLifecycle(ctx, authorization.Actor{UserID: admin}, asset.ID, false)
					} else if lifecycle == "unlink" {
						w := httptest.NewRecorder()
						router.ServeHTTP(w, unlinkRequest)
						unlinkConflict = w.Code == 409
						if w.Code != 303 && w.Code != 409 {
							archive <- fmt.Errorf("unlink: %d %s", w.Code, w.Body.String())
						} else {
							archive <- nil
						}
					} else {
						archive <- auth.ChangeAssetAssociation(ctx, authorization.Actor{UserID: admin}, asset.ID, authorization.AssetAssociationInput{Action: "transfer", CustomerID: next.ID, EffectiveDate: time.Now().UTC().Format("2006-01-02"), Reason: "Racing departure", Revision: 0})
					}
				}()
				go func() {
					<-start
					revise <- serviceAPIRequest(api, token, "POST", target, command)
				}()
				close(start)
				if err = <-archive; err != nil {
					t.Fatal(err)
				}
				response = <-revise
				if response.Code != 200 && response.Code != 409 {
					t.Fatalf("revision/archive=%d %s", response.Code, response.Body.String())
				}
				// A newly accepted entry changes the Job's permanent blockers and
				// can stale the office form. Explicitly reread that form before
				// completing unlink; never bypass the production save witness.
				if unlinkConflict {
					fieldPost(t, router, cookie, unlinkPath, unlinkValues, 303)
				}
				var invalid int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_revisions r JOIN asset_service_entries e ON e.id=r.entry_id JOIN assets a ON a.id=e.asset_id WHERE r.actor_id<>$1 OR r.accepted_at>a.deleted_at`, admin).Scan(&invalid); err != nil || invalid != 0 {
					t.Fatalf("unattributable/late revisions=%d err=%v", invalid, err)
				}
				if lifecycle == "unlink" {
					historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_revisions r JOIN asset_service_entries e ON e.id=r.entry_id JOIN job_asset_links l ON l.id=e.link_id WHERE e.job_id=$1 AND (l.unlinked_at IS NULL OR r.accepted_at>l.unlinked_at)`, job.ID).Scan(&invalid))
					if invalid != 0 {
						t.Fatalf("unlinked relationship accepted late revisions: %d", invalid)
					}
				}
				command["operation_id"] = newOperationKey()
				response = serviceAPIRequest(api, token, "POST", target, command)
				if response.Code != 409 {
					t.Fatalf("archived mutation=%d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}
