package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestProtectedAuditLongRecordedRoleFilterHTTP(t *testing.T) {
	for _, tc := range []struct{ name, role string }{
		{"ASCII", strings.Repeat("r", 201)},
		{"multibyte", strings.Repeat("界", 67)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, actor, 0, tc.role, grants)
			if err != nil {
				t.Fatal(err)
			}
			user := client.User.Create().SetCompanyID(company).SetEmail("long-role-audit@test").SetName("Long role author").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, user.ID)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			for i := range 26 {
				fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {fmt.Sprintf("Long role customer %d", i)}}, 303)
			}
			type evidence struct{ id, target string }
			// Expected identities come from persisted HTTP effects, independently of the projection.
			recorded := func(name string, count int) []evidence {
				t.Helper()
				rows, err := pool.Query(ctx, `SELECT id::text,target_id::text FROM authorization_audit WHERE actor_id=$1 AND action='customer.create' AND role_name=$2 ORDER BY authorization_audit.id`, user.ID, name)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var out []evidence
				for rows.Next() {
					var row evidence
					if err := rows.Scan(&row.id, &row.target); err != nil {
						t.Fatal(err)
					}
					out = append(out, row)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				if len(out) != count {
					t.Fatalf("recorded events = %d, want %d", len(out), count)
				}
				return out
			}
			want := recorded(tc.role, 26)
			var revision int64
			if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			renamed := tc.role + " renamed"
			if _, err := auth.SaveRole(ctx, actor, role, renamed, grants, revision); err != nil {
				t.Fatal(err)
			}
			fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Customer after Role rename"}}, 303)
			wantRenamed := recorded(renamed, 1)
			filters := url.Values{"permission": {"customer.create"}, "role": {tc.role}, "sort": {"oldest"}, "per_page": {"25"}}
			checkRows := func(t *testing.T, rows [][]string, want []evidence, name string) {
				t.Helper()
				if len(rows) != len(want) {
					t.Fatalf("evidence rows = %d, want %d", len(rows), len(want))
				}
				for i, row := range rows {
					if len(row) != 23 {
						t.Fatalf("row %d has %d columns", i, len(row))
					}
					if row[0] != want[i].id || row[3] != fmt.Sprint(user.ID) || row[6] != fmt.Sprint(role) || row[8] != name || row[9] != "customer.create" || row[11] != "customer" || row[12] != want[i].target {
						t.Fatalf("unexpected evidence row %d: %#v; want %+v, Role %q", i, row, want[i], name)
					}
				}
			}
			t.Run("browse", func(t *testing.T) {
				rowRE := regexp.MustCompile(`(?s)<tr>\s*<td>.*?</tr>`)
				cellRE := regexp.MustCompile(`(?s)<td>(.*?)</td>`)
				readPage := func(path string) (string, [][]string) {
					t.Helper()
					body := requestBody(t, router, adminCookie, "GET", path, 200)
					var rows [][]string
					for _, raw := range rowRE.FindAllString(body, -1) {
						var row []string
						for _, cell := range cellRE.FindAllStringSubmatch(raw, -1) {
							row = append(row, html.UnescapeString(cell[1]))
						}
						rows = append(rows, row)
					}
					return body, rows
				}
				body, rows := readPage("/settings/audit?" + filters.Encode())
				checkRows(t, rows, want[:25], tc.role)
				next := regexp.MustCompile(`href="([^"]+)">Next page</a>`).FindStringSubmatch(body)
				if len(next) != 2 {
					t.Fatal("missing next-page link")
				}
				nextURL, err := url.Parse(html.UnescapeString(next[1]))
				if err != nil {
					t.Fatal(err)
				}
				if nextURL.Path != "/settings/audit" || nextURL.Query().Get("role") != tc.role || nextURL.Query().Get("after") != want[24].id {
					t.Fatalf("incorrect next-page URL: %s", nextURL)
				}
				body, rows = readPage(nextURL.String())
				checkRows(t, rows, want[25:], tc.role)
				assertNotContains(t, body, "Next page")
				_, rows = readPage("/settings/audit?" + url.Values{"permission": {"customer.create"}, "role": {renamed}, "sort": {"oldest"}}.Encode())
				checkRows(t, rows, wantRenamed, renamed)
			})
			t.Run("export", func(t *testing.T) {
				review := requestBody(t, router, adminCookie, "GET", "/settings/audit/export?"+filters.Encode(), 200)
				assertContains(t, review, "All matching rows")
				assertContains(t, review, `name="role" value="`+tc.role+`"`)
				// Browse cursor/page size cannot restrict an all-matching export.
				values := url.Values{"operation_id": {newOperationKey()}, "permission": {"customer.create"}, "role": {tc.role}, "sort": {"oldest"}, "per_page": {"25"}, "after": {want[24].id}}
				path := masterPost(t, router, adminCookie, "/settings/audit/export", values, 303)
				if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
					t.Fatal(err)
				}
				body := requestBody(t, router, adminCookie, "GET", path+"/download", 200)
				rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != len(want)+1 {
					t.Fatalf("CSV rows = %d, want %d", len(rows), len(want)+1)
				}
				if len(rows[0]) != 23 || rows[0][0] != "audit_id" || rows[0][8] != "event_role_name" || rows[0][12] != "target_id" {
					t.Fatalf("unexpected CSV header: %#v", rows[0])
				}
				checkRows(t, rows[1:], want, tc.role)
				var status exports.Status
				masterJSON(t, router, adminCookie, path, &status)
				if status.RowCount == nil || *status.RowCount != 26 || status.AsOf == nil {
					t.Fatalf("incorrect export totals: %+v", status)
				}
			})
		})
	}
}
