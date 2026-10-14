package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/status"
)

// Archive is performed through the production office action while the assigned
// Job continues. Both context routes must retain only the authorized projection.
func TestAssignedContextArchivedProject(t *testing.T) {
	for _, category := range []string{"completed", "canceled"} {
		t.Run(category, func(t *testing.T) {
			f := associationRepair(t)
			ctx := context.Background()
			c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Archive context Customer").SaveX(ctx)
			l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("SENSITIVE-LOCATION").SaveX(ctx)
			p := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetName("Retained Project").SetDescription("SENSITIVE-DESCRIPTION").SetServiceNotes("SENSITIVE-SERVICE").SetCompletionPercentage(37).SaveX(ctx)
			j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Continuing assigned Job").SaveX(ctx)
			grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "project.view", Scope: authorization.Context}}
			uid, role, cookie := f.reader(t, grants)
			f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
			token, err := f.sessions.CreateMobile(ctx, uid, "Archive context reference")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(f.pool, f.client, f.sessions)
			base := fmt.Sprintf("/jobs/%d", j.ID)
			get := func() map[string]any {
				t.Helper()
				r := httptest.NewRequest("GET", base+"/master-context", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("context: %d %s", w.Code, w.Body.String())
				}
				var envelope struct {
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				return envelope.Data
			}
			active := get()
			if _, ok := active["project_reference"]; ok {
				t.Fatal("active projection changed")
			}
			assertContains(t, fmt.Sprint(active["project"]), "SENSITIVE-DESCRIPTION")
			target := f.client.Status.Query().Where(status.CompanyIDEQ(f.company), status.CategoryKeyEQ("project:"+category)).FirstX(ctx)
			path := fmt.Sprintf("/projects/%d", p.ID)
			masterPost(t, f.router, f.cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(target.ID)}}, 303)
			masterPost(t, f.router, f.cookie, path+"/delete", nil, 303)
			bounded := func() {
				t.Helper()
				got := get()
				want := map[string]any{"project_reference": map[string]any{"name": "Retained Project", "archived": true}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("archived projection = %#v; want bounded reference %#v", got, want)
				}
				for _, suffix := range []string{"/master-context", "/customer-context"} {
					body := requestBody(t, f.router, cookie, "GET", base+suffix, 200)
					assertContains(t, body, "Retained Project")
					assertContains(t, body, "Archived")
					assertNotContains(t, body, "SENSITIVE-")
					assertNotContains(t, body, "href=\"/projects/")
					assertNotContains(t, body, "37%")
					assertNotContains(t, body, "100%")
				}
			}
			browser := func(mode string) {
				t.Helper()
				if os.Getenv("BROWSER_TESTS") != "1" {
					return
				}
				server := httptest.NewServer(f.router)
				defer server.Close()
				output, err := exec.Command("node", "testdata/browser/assigned-context-archive.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), mode).CombinedOutput()
				if err != nil {
					t.Errorf("browser: %v %s", err, output)
				} else {
					t.Log(string(output))
				}
			}
			browser("bounded")
			bounded()
			// Archived authority and context authority are independent, including loss
			// on the next request. No grant is inferred from the relationship.
			withArchive := append(append([]authorization.Grant{}, grants...), authorization.Grant{Permission: "project.archived.view", Scope: authorization.All})
			withArchive[2].Scope = authorization.All
			svc := authorization.New(f.pool)
			save := func(g []authorization.Grant, revision int64) {
				t.Helper()
				if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", g, revision); err != nil {
					t.Fatal(err)
				}
			}
			save(withArchive, 1)
			assertContains(t, fmt.Sprint(get()["project"]), "SENSITIVE-DESCRIPTION")
			assertContains(t, requestBody(t, f.router, cookie, "GET", base+"/master-context", 200), "SENSITIVE-SERVICE")
			browser("detail")
			// Broad base View alone still does not authorize archived details.
			save(withArchive[:3], 2)
			bounded()
			browser("bounded")
			// Grant catalog requires broad View for archived View; exercise absence of
			// both Project grants rather than inventing an invalid grant combination.
			save(grants[:2], 3)
			if got := get(); len(got) != 0 {
				t.Fatalf("lost Project authority: %v", got)
			}
			browser("absent")
			save(grants, 4)
			masterPost(t, f.router, f.cookie, path+"/restore", nil, 303)
			restored := get()
			assertContains(t, fmt.Sprint(restored["project"]), "SENSITIVE-DESCRIPTION")
			if _, ok := restored["project_reference"]; ok {
				t.Fatal("restore retained archived reference")
			}
			// The same explicit link survives archive/restore; no repair/relink action.
			if got := f.client.Job.GetX(ctx, j.ID); got.ProjectID == nil || *got.ProjectID != p.ID || got.DeletedAt != nil {
				t.Fatal("archive changed continuing Job relationship")
			}
		})
	}
}
