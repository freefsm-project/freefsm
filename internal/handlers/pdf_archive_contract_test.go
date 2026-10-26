package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestPDFArchiveIndependentGrantsAndExactSelectionHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive PDF customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Archive PDF Job").SaveX(ctx)
			fieldPost(t, web, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Archive PDF"}, "line_items": {"[]"}}, 303)
			api := apiv1.NewRouter(pool, client, sessions)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, "SELECT id FROM "+kind+"s").Scan(&id))
			path := fmt.Sprintf("/%ss/%d", kind, id)
			if kind == "invoice" {
				fieldPost(t, web, cookie, path+"/finalize", commercialReviewedValues(t, web, cookie, path, url.Values{}), 303)
			}
			fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
			original := requestBody(t, web, cookie, "GET", path+"/pdf", 200)
			var version int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version))
			fieldPost(t, web, cookie, path+"/delete", url.Values{}, 303)
			auth := authorization.New(pool)
			for n, extra := range [][]string{{}, {"pdf.download"}, {"archived.view"}, {"archived.view", "pdf.download"}} {
				grants := []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".pdf.generate", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}}
				for _, permission := range extra {
					grants = append(grants, authorization.Grant{Permission: kind + "." + permission, Scope: authorization.All})
				}
				role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("Archive PDF %d", n), grants)
				historyCheck(t, err)
				u := client.User.Create().SetCompanyID(company).SetEmail(fmt.Sprintf("archive-pdf-%d@test", n)).SetName("PDF reader").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
				historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
				reader := sessionCookie(t, ctx, sessions, u.ID)
				want := 404
				if n == 2 {
					want = 403
				}
				if n == 3 {
					want = 200
				}
				expectStatus(t, web, reader, "GET", fmt.Sprintf("%s/pdf?version=%d", path, version), want)
				generation := 404
				if n >= 2 {
					generation = 403
				}
				expectStatus(t, web, reader, "POST", path+"/pdf/save", generation)
				token, err := sessions.CreateMobile(ctx, u.ID, "Archived PDF client")
				historyCheck(t, err)
				apiCall := func(method, url, body string, want int) {
					t.Helper()
					r := httptest.NewRequest(method, url, strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					if w.Code != want {
						t.Fatalf("archived API %s %s=%d want %d: %s", method, url, w.Code, want, w.Body.String())
					}
				}
				apiCall("GET", fmt.Sprintf("%s/pdf/versions/%d", path, version), "", want)
				apiCall("GET", path+"/pdf/source", "", generation)
				apiCall("POST", path+"/pdf/generate", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":"retained-review","depends_on":[]}`, newOperationKey(), job.ID), generation)
				if n < 2 {
					expectStatus(t, web, reader, "GET", path+"/pdf/preview", 404)
				}
				// Visible Customer context never grants access to its archived PDF.
				expectStatus(t, web, reader, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
			}
			if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, version), 200); got != original {
				t.Fatal("archive changed retained PDF")
			}
			fieldPost(t, web, cookie, path+"/restore", url.Values{}, 303)
			fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
			// Explicitly stale old selection is warned and audited by exact ID.
			_, err := pool.Exec(ctx, "UPDATE "+kind+"s SET notes='Rendered drift' WHERE id=$1", id)
			historyCheck(t, err)
			assertContains(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "This PDF is stale")
			expectStatus(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, version), 409)
			if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200); got != original {
				t.Fatal("stale download substituted bytes")
			}
			var audited bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE action=$1 AND target_id=$2 AND detail->>'pdf_version'=$3 AND detail->>'stale_confirmed'='true')`, kind+".pdf.download", id, fmt.Sprint(version)).Scan(&audited))
			if !audited {
				t.Fatal("missing exact selected stale confirmation audit")
			}
			expectStatus(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version+1000), 404)
		})
	}
}
