package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestJobRelationshipLookupStartupExpiryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Startup private owner").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Startup private contact").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetCustomerContactID(contact.ID).SetJobType("Startup private work").SaveX(ctx)
	server := newInteractionBrowserServer(t, router)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/job-lookup-startup.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(j.ID), fmt.Sprint(c.ID), fmt.Sprint(contact.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	got := client.Job.GetX(ctx, j.ID)
	if got.CustomerContactID == nil || *got.CustomerContactID != contact.ID || got.CustomerID != c.ID || got.JobType != j.JobType {
		t.Fatal("expiry or late lookup response changed the persisted Job")
	}
}
