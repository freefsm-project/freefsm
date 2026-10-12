package handlers

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestJobRelationshipLookupFailurePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	a := client.Customer.Create().SetCompanyID(company).SetDisplayName("Existing Customer A").SaveX(ctx)
	b := client.Customer.Create().SetCompanyID(company).SetDisplayName("Different Customer B").SaveX(ctx)
	contactA := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(a.ID).SetFirstName("Existing linked contact").SaveX(ctx)
	contactB := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(b.ID).SetFirstName("Other customer contact").SaveX(ctx)
	projectA := client.Project.Create().SetCompanyID(company).SetCustomerID(a.ID).SetName("Existing linked project").SaveX(ctx)
	projectB := client.Project.Create().SetCompanyID(company).SetCustomerID(b.ID).SetName("Other customer project").SaveX(ctx)
	locationA := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(a.ID).SetTitle("Existing linked location").SaveX(ctx)
	locationB := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(b.ID).SetTitle("Other customer location").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(a.ID).SetCustomerContactID(contactA.ID).SetProjectID(projectA.ID).SetLocationID(locationA.ID).SetJobType("Existing linked Job").SaveX(ctx)
	fixture, err := json.Marshal(map[string]int64{"job": job.ID, "a": a.ID, "b": b.ID, "contactA": contactA.ID, "contactB": contactB.ID, "projectA": projectA.ID, "projectB": projectB.ID, "locationA": locationA.ID, "locationB": locationB.ID})
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	server := newInteractionBrowserServer(t, router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/job-lookups.cjs", server.URL, cookie.Value, string(fixture)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
