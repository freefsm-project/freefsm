package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestConversionProvenanceHistoryAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	f.api = conversionContract(t, f.api)
	source := f.create(t)
	first := batchResults(t, f, f.operation(source, "convert", map[string]any{}))[0]
	batchOutcome(t, first, "accepted")
	id := int64(first.Receipt["id"].(float64))
	assertContains(t, lifecycleCall(t, f.api, f.token, "GET", fmt.Sprintf("/invoices/%d", id), "", 200), `"provenance":true`)
	path := fmt.Sprintf("/invoices/%d/conversion-history", id)
	read := func(path string) []authorization.ActivityRecord {
		t.Helper()
		body := lifecycleCall(t, f.api, f.token, "GET", path, "", 200)
		var page struct {
			Data struct {
				Records []authorization.ActivityRecord `json:"records"`
			} `json:"data"`
		}
		historyCheck(t, json.Unmarshal([]byte(body), &page))
		if strings.Contains(body, "source_snapshot") || strings.Contains(body, "metadata") {
			t.Fatal("raw conversion history disclosed")
		}
		return page.Data.Records
	}
	events := read(path)
	if len(events) == 0 {
		t.Fatal("conversion history missing")
	}
	hidden := false
	for _, e := range events {
		if e.Type == "estimate" && e.RecordID == source.ID && e.Action == "estimate_converted" {
			hidden = e.ProvenanceEstimateID == source.ID && len(e.Facts) == 0
		}
	}
	if !hidden {
		t.Fatalf("missing bounded hidden-parent provenance: %+v", events)
	}
	lifecycleCall(t, f.api, f.token, "GET", fmt.Sprintf("/estimates/%d", source.ID), "", 404)
	invoice := f
	invoice.kind = "invoice"
	revert := invoice.operation(matrixState(t, invoice.read(t, id)), "revert", map[string]any{"cycle_id": first.Receipt["cycle_id"]})
	batchOutcome(t, batchResults(t, invoice, revert)[0], "accepted")
	events = read(fmt.Sprintf("/estimates/%d/conversion-history", source.ID))
	if len(events) < 2 {
		t.Fatal("reversion lost conversion history")
	}
	second := batchResults(t, f, f.operation(matrixState(t, f.read(t, source.ID)), "convert", map[string]any{}))[0]
	batchOutcome(t, second, "accepted")
	events = read(fmt.Sprintf("/invoices/%d/conversion-history", int64(second.Receipt["id"].(float64))))
	found := false
	for _, e := range events {
		if e.RecordID == id && e.Type == "invoice" && e.Action == "invoice_conversion_reverted" {
			found = true
		}
	}
	if !found {
		t.Fatal("reconversion lost prior Invoice history")
	}
	pagePath := fmt.Sprintf("/invoices/%d/conversion-history?limit=1", int64(second.Receipt["id"].(float64)))
	seen := map[int64]bool{}
	for next, pages := pagePath, 0; next != ""; pages++ {
		if pages > len(events) {
			t.Fatal("history cursor did not terminate")
		}
		var page struct {
			Data struct {
				Records []authorization.ActivityRecord `json:"records"`
				Next    string                         `json:"next_cursor"`
			} `json:"data"`
		}
		historyCheck(t, json.Unmarshal([]byte(lifecycleCall(t, f.api, f.token, "GET", next, "", 200)), &page))
		if len(page.Data.Records) != 1 || seen[page.Data.Records[0].ID] {
			t.Fatal("history page repeated or exceeded limit")
		}
		seen[page.Data.Records[0].ID] = true
		next = ""
		if page.Data.Next != "" {
			next = pagePath + "&cursor=" + page.Data.Next
			lifecycleCall(t, f.api, f.token, "GET", next+"&action=estimate_converted", "", 400)
		}
	}
	if len(seen) != len(events) {
		t.Fatal("history pagination lost events")
	}
}

func TestConversionProvenanceAuthorityAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("History customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("History job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "History setup")
	historyCheck(t, err)
	f := batchCommercialFixture{pool: pool, api: conversionContract(t, apiv1.NewRouter(pool, client, sessions)), token: token, kind: "estimate", job: j.ID, customer: c.ID, company: company, user: admin}
	source := f.create(t)
	result := batchResults(t, f, f.operation(source, "convert", map[string]any{}))[0]
	batchOutcome(t, result, "accepted")
	path := fmt.Sprintf("/invoices/%d/conversion-history", int64(result.Receipt["id"].(float64)))
	u := client.User.Create().SetCompanyID(company).SetName("History viewer").SetEmail("history-viewer@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "History viewer", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	assignFieldDocumentCreator(t, pool, j.ID, u.ID)
	token, err = sessions.CreateMobile(ctx, u.ID, "History viewer")
	historyCheck(t, err)
	lifecycleCall(t, f.api, token, "GET", path, "", 403)
	grants = append(grants, authorization.Grant{Permission: "invoice.provenance.view", Scope: authorization.Assigned})
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "History viewer", grants, revision)
	historyCheck(t, err)
	body := lifecycleCall(t, f.api, token, "GET", path, "", 200)
	assertContains(t, body, `"provenance_estimate_id":`)
	lifecycleCall(t, f.api, token, "GET", fmt.Sprintf("/estimates/%d", source.ID), "", 404)
	for _, query := range []string{"?unknown=true", "?limit=0", "?limit=201", "?limit=1&limit=2", "?cursor=invalid", "?action="} {
		lifecycleCall(t, f.api, token, "GET", path+query, "", 400)
	}
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
	historyCheck(t, err)
	lifecycleCall(t, f.api, token, "GET", path, "", 403)
}
