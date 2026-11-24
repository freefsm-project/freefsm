package handlers

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestFinancialAcceptanceExpiryAndFilterPreservingRecovery(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery selection").SaveX(ctx)
	for _, output := range financialAcceptanceOutputs {
		t.Run(output.view, func(t *testing.T) {
			values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}
			if output.view == "combined" || output.view == "settlements" {
				values.Set("from", "2020-01-01")
				values.Set("to", "2020-01-31")
			}
			path := financialAcceptancePost(t, web, cookie, output.export, values, 303)
			_, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			requestBody(t, web, cookie, "GET", path+"/download", 200)
			_, err = pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/"))
			historyCheck(t, err)
			requestBody(t, web, cookie, "GET", path+"/download", 410)
			body := requestBody(t, web, cookie, "GET", path, 200)
			match := regexp.MustCompile(`href="([^"]+)">Review a new export with these filters and scope</a>`).FindStringSubmatch(body)
			if len(match) != 2 {
				t.Fatalf("missing authorized recovery for %s", output.view)
			}
			u, err := url.Parse(html.UnescapeString(match[1]))
			historyCheck(t, err)
			if u.Path != output.export || u.Query().Get("customer_id") != fmt.Sprint(c.ID) || u.Query().Get("from") != values.Get("from") || u.Query().Get("to") != values.Get("to") {
				t.Fatalf("lost recovery selection: %s", u)
			}
			review := requestBody(t, web, cookie, "GET", u.String(), 200)
			operation := regexp.MustCompile(`name="operation_id" value="([^"]+)"`).FindStringSubmatch(review)
			if len(operation) != 2 || operation[1] == values.Get("operation_id") {
				t.Fatal("recovery reused expired intent")
			}
			values.Set("operation_id", operation[1])
			fresh := financialAcceptancePost(t, web, cookie, output.export, values, 303)
			if fresh == path {
				t.Fatal("recovery extended original receipt")
			}
			_, err = exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			requestBody(t, web, cookie, "GET", fresh+"/download", 200)
			requestBody(t, web, cookie, "GET", path+"/download", 410)
		})
	}
}
