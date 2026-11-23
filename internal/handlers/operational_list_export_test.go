package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestOperationalListExportAllMatchingRowsAndCSVHTTP(t *testing.T) {
	for _, kind := range []string{"jobs", "estimates", "invoices"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("CSV Customer").SaveX(ctx)
			const unusual = "Needle \"quoted\", line\nbreak"
			for n := 0; n < 135; n++ {
				title := fmt.Sprintf("Needle %03d", n)
				if n == 134 {
					title = unusual
				}
				switch kind {
				case "jobs":
					client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType(title).SaveX(ctx)
				case "estimates":
					client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle(title).SaveX(ctx)
				case "invoices":
					client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle(title).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
				}
			}
			// A different selected set must not be silently widened by the worker.
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unrelated secret").SaveX(ctx)
			base := "/" + kind
			listBody := requestBody(t, web, cookie, "GET", base+"?search=Needle", 200)
			assertContains(t, listBody, "Needle")
			requestBody(t, web, cookie, "GET", base+"/export?search=Needle&scope=A", 200)
			path := masterPost(t, web, cookie, base+"/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "search": {"Needle"}}, 303)
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Pending")
			_, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			body := requestBody(t, web, cookie, "GET", path+"/download", 200)
			rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
			historyCheck(t, err)
			if len(rows) != 136 {
				t.Fatalf("export must include 135 matching records beyond a page; got %d", len(rows)-1)
			}
			idColumn := -1
			for i, column := range rows[0] {
				if column == "job_id" && kind == "jobs" || column == "document_id" && kind != "jobs" {
					idColumn = i
				}
			}
			if idColumn < 0 {
				t.Fatalf("missing stable identity column: %v", rows[0])
			}
			seen := map[string]bool{}
			position := 0
			for _, match := range regexp.MustCompile(`href="`+base+`/([0-9]+)"`).FindAllStringSubmatch(listBody, -1) {
				if seen[match[1]] {
					continue
				}
				seen[match[1]] = true
				position++
				if position >= len(rows) || rows[position][idColumn] != match[1] {
					t.Fatalf("list/export ordering differs at %d: list ID %s", position, match[1])
				}
			}
			if position == 0 || position >= 135 {
				t.Fatalf("fixture did not exercise a bounded list page: %d rows", position)
			}
			found := false
			for _, row := range rows[1:] {
				for _, cell := range row {
					if cell == unusual {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("CSV did not roundtrip quotes, comma and newline: %s", body)
			}
			for _, forbidden := range []string{"Unrelated secret", "unit_price", "subtotal", "tax_amount", "grand_total", "latitude", "longitude"} {
				assertNotContains(t, body, forbidden)
			}
			empty := masterPost(t, web, cookie, base+"/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "search": {"No matching source"}}, 303)
			_, err = exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			emptyBody := requestBody(t, web, cookie, "GET", empty+"/download", 200)
			emptyRows, err := csv.NewReader(strings.NewReader(emptyBody)).ReadAll()
			historyCheck(t, err)
			if len(emptyRows) != 1 || strings.Join(emptyRows[0], ",") != strings.Join(rows[0], ",") {
				t.Fatalf("empty successful export lost fixed contract: %v", emptyRows)
			}
		})
	}
}

func TestOperationalListExportIndependentViewAndExportHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	for _, kind := range []struct{ path, permission string }{{"jobs", "job"}, {"estimates", "estimate"}, {"invoices", "invoice"}} {
		u := client.User.Create().SetCompanyID(company).SetEmail(kind.path + "-reader@test").SetName("Read only").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
		role, err := auth.SaveRole(ctx, actor, 0, kind.path+" read only", []authorization.Grant{{Permission: kind.permission + ".view", Scope: authorization.All}})
		historyCheck(t, err)
		historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		requestBody(t, web, cookie, "GET", "/"+kind.path, 200)
		expectStatus(t, web, cookie, "GET", "/"+kind.path+"/export?scope=A", 403)
		masterPost(t, web, cookie, "/"+kind.path+"/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "search": {"no matches"}}, 403)
	}
	var accepted int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&accepted))
	if accepted != 0 {
		t.Fatal("read-only actors accepted exports, including empty selection")
	}
}

func TestOperationalListExportAssignedScopeAndWholeArtifactRevocationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u := client.User.Create().SetCompanyID(company).SetName("Scoped exporter").SetEmail("scoped-export@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{}
	for _, kind := range []string{"job", "estimate", "invoice"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All}, authorization.Grant{Permission: kind + ".export", Scope: authorization.Assigned})
	}
	role, err := auth.SaveRole(ctx, actor, 0, "All view assigned export", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Scope Customer").SaveX(ctx)
	owned := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned visible Job").SaveX(ctx)
	other := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unassigned secret Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(owned.ID).SetUserID(u.ID).SaveX(ctx)
	for _, j := range []struct {
		id    int64
		title string
	}{{owned.ID, "Assigned document"}, {other.ID, "Unassigned secret document"}, {0, "Customer only secret"}} {
		e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle(j.title)
		i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle(j.title).SetInvoiceDate(time.Now()).SetDueDate(time.Now())
		if j.id != 0 {
			e.SetJobID(j.id)
			i.SetJobID(j.id)
		}
		e.SaveX(ctx)
		i.SaveX(ctx)
	}
	var paths []string
	for _, kind := range []string{"jobs", "estimates", "invoices"} {
		expectStatus(t, web, cookie, "GET", "/"+kind+"/export?scope=A", 403)
		path := masterPost(t, web, cookie, "/"+kind+"/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"J"}}, 303)
		_, err := exports.Product(pool).ProcessOne(ctx)
		historyCheck(t, err)
		body := requestBody(t, web, cookie, "GET", path+"/download", 200)
		assertContains(t, body, "Assigned")
		assertNotContains(t, body, "Unassigned secret")
		assertNotContains(t, body, "Customer only secret")
		paths = append(paths, path)
	}
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, owned.ID, u.ID)
	historyCheck(t, err)
	for _, path := range paths {
		expectStatus(t, web, cookie, "GET", path+"/download", 410)
	}
}
