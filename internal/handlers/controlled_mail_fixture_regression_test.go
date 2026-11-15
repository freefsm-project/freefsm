package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestControlledMailInstanceSharedOwner(t *testing.T) {
	f := permissionHTTPControlledMailInstanceFixture(t)
	ctx := context.Background()
	policy, err := emailpolicy.Read(ctx, f.pool, false)
	historyCheck(t, err)
	if !policy.Enabled {
		t.Fatal("fresh instance import did not preserve enabled mail")
	}
	_, release, err := f.control.EnterEmail(ctx)
	historyCheck(t, err)
	release()
	cookie := sessionCookie(t, ctx, f.sessions, f.admin)
	customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Shared policy").SaveX(ctx)
	doc := f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetTitle("Queued before denial").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	_, err = f.pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost'`)
	historyCheck(t, err)
	expectStatus(t, f.web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, f.web, cookie, "GET", path+"/pdf/preview", 200), "version")
	fieldPost(t, f.web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Shared owner"}, "body": {"Immutable"}}, 303)
	before := historyDurableRows(t, f.pool, []string{"document_deliveries"})
	for _, unavailable := range []bool{false, true} {
		_, err = f.pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false;`)
		historyCheck(t, err)
		_, err = f.pool.Exec(ctx, `UPDATE restore_email_policy_import SET complete=$1`, !unavailable)
		historyCheck(t, err)
		want := instancecontrol.ErrEmailDisabled
		if unavailable {
			want = instancecontrol.ErrEmailUnavailable
		}
		capture := &compositionSender{}
		// Disabled is an idle worker, while unavailable is an operational error.
		var workerError error
		if unavailable {
			workerError = want
		}
		if worked, err := f.outbox.ProcessOne(ctx, capture, nil); worked || !errors.Is(err, workerError) || len(capture.messages) != 0 {
			t.Fatalf("worker bypassed shared policy: worked=%v err=%v messages=%d", worked, err, len(capture.messages))
		}
		historyEqual(t, "denied worker preserves queued delivery", historyDurableRows(t, f.pool, []string{"document_deliveries"}), before)
		if err := f.email.SendTestEmail(ctx, "recipient@example.test", "Recipient"); !errors.Is(err, want) {
			t.Fatalf("direct email policy=%v want=%v", err, want)
		}
		response := onboardingPost(f.web, cookie, "/settings/test-email", url.Values{"recipient": {"recipient@example.test"}})
		if response.Code != 503 {
			t.Fatalf("web policy=%d body=%s", response.Code, response.Body.String())
		}
	}
	// The API must also use this instance's admission owner, rather than a second
	// control that happens to read the same enabled flag.
	startup, err := f.manager.BeginStartup(ctx)
	historyCheck(t, err)
	defer startup.Close()
	for name, router := range map[string]http.Handler{"web": f.web, "api": f.api} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/customers", nil))
		if response.Code != 503 {
			t.Fatalf("%s did not share maintenance owner: %d", name, response.Code)
		}
	}
}
