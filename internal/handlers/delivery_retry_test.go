package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
)

func TestDeliveryManualRetryUsesCurrentIndependentGrantsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retry customer").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Retry frozen").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	conversionUpload(t, web, adminCookie, "estimate", doc.ID, "retry.txt", "original retry File bytes")
	var file int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&file))
	expectStatus(t, web, adminCookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, adminCookie, "GET", path+"/pdf/preview", 200), "version")
	fieldPost(t, web, adminCookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"retry@example.test"}, "subject": {"Frozen"}, "body": {"Body"}, "extra_file_id": {fmt.Sprint(file)}}, 303)
	svc := delivery.New(pool, "")
	_, err = svc.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
		return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("owned rejection")}
	}), nil)
	historyCheck(t, err)
	var id int64
	var originalMessage string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,message_id FROM document_deliveries`).Scan(&id, &originalMessage))
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.delivery.request", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Explicit retry authority", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetEmail("retry@test").SetName("Retry user").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	retry := fmt.Sprintf("%s/deliveries/%d/retry", path, id)
	command := url.Values{"idempotency_key": {newOperationKey()}, "reason": {"explicit retry of frozen communication"}}
	fieldPost(t, web, cookie, retry, command, 403)
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	grants = append(grants, authorization.Grant{Permission: "estimate.pdf.download", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, actor, role, "Explicit retry authority", grants, revision)
	historyCheck(t, err)
	fieldPost(t, web, cookie, retry, command, 403)
	grants = append(grants, authorization.Grant{Permission: "file.download", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, actor, role, "Explicit retry authority", grants, revision+1)
	historyCheck(t, err)
	fieldPost(t, web, cookie, retry, command, 303)
	fieldPost(t, web, cookie, retry, command, 303)
	var attempts, cycle int
	historyCheck(t, pool.QueryRow(ctx, `SELECT lifetime_attempt_count,cycle_attempt_count FROM document_deliveries WHERE id=$1`, id).Scan(&attempts, &cycle))
	if attempts != 1 || cycle != 1 {
		t.Fatal("manual retry reset recorded automatic budget")
	}
	capture := &compositionSender{}
	worked, err := svc.ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if !worked || len(capture.messages) != 1 {
		t.Fatal("authorized frozen retry missing")
	}
	sent := capture.messages[0]
	if sent.MessageID != originalMessage || len(sent.Attachments) != 1 || string(sent.Attachments[0].Data) != "original retry File bytes" {
		t.Fatal("retry substituted immutable content")
	}
	command.Set("idempotency_key", newOperationKey())
	fieldPost(t, web, cookie, retry, command, 303)
	worked, err = svc.ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if worked {
		t.Fatal("retry of Accepted created more mail")
	}
}
