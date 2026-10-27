package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestProjectArchiveSemanticEligibilityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Project archive owner").SaveX(ctx)
	for _, category := range []string{"new", "in_progress", "pending", "completed", "canceled"} {
		t.Run(category, func(t *testing.T) {
			var status int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, "project:"+category).Scan(&status))
			p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetCompletionPercentage(100).SetName("Progress alone is not completion").SaveX(ctx)
			// A continuing, scheduled Job must remain unchanged by Project archive.
			start := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Continuing scheduled work").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
			var before, after string
			historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&before))
			want := 409
			if category == "completed" || category == "canceled" {
				want = 303
			}
			fieldPost(t, web, cookie, fmt.Sprintf("/projects/%d/delete", p.ID), nil, want)
			historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&after))
			if before != after {
				t.Fatal("Project archive changed continuing Job or its schedule")
			}
		})
	}
}

func TestWorkArchiveVocabularyCannotReopenHistoryHTTP(t *testing.T) {
	for _, kind := range []string{"job", "project"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Vocabulary owner").SaveX(ctx)
			settings := "/settings/statuses/" + kind
			fieldPost(t, web, cookie, settings, url.Values{"name": {"Custom finished"}, "color": {"#123456"}, "category": {kind + ":completed"}}, 303)
			var status, id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND name='Custom finished'`, company, kind+":completed").Scan(&status))
			if kind == "job" {
				id = client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Closed work").SaveX(ctx).ID
			} else {
				id = client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetName("Closed work").SaveX(ctx).ID
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			fieldPost(t, web, cookie, path+"/delete", nil, 303)
			// A cosmetic rename must remain legal and must not change eligibility.
			fieldPost(t, web, cookie, fmt.Sprintf("%s/%d", settings, status), url.Values{"name": {"Looks unfinished"}, "color": {"#123456"}}, 303)
			before := historyDurableRows(t, pool, []string{kind + "s", "statuses", "authorization_audit", "activity_logs"})
			fieldPost(t, web, cookie, fmt.Sprintf("%s/%d/move", settings, status), url.Values{"category": {kind + ":in_progress"}, "order": {"1"}, "confirm_in_use": {"true"}}, 409)
			historyEqual(t, "vocabulary cannot reopen archived work", historyDurableRows(t, pool, []string{kind + "s", "statuses", "authorization_audit", "activity_logs"}), before)
			fieldPost(t, web, cookie, path+"/restore", nil, 303)
			fieldPost(t, web, cookie, fmt.Sprintf("%s/%d/move", settings, status), url.Values{"category": {kind + ":in_progress"}, "order": {"1"}, "confirm_in_use": {"true"}}, 303)
		})
	}
}

func TestWorkArchiveReopenCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"job", "project"} {
		for _, archiveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archiveFirst=%v", kind, archiveFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reopen race owner").SaveX(ctx)
				var finished, unfinished, id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, kind+":completed").Scan(&finished))
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, kind+":new").Scan(&unfinished))
				if kind == "job" {
					id = client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(finished).SetJobType("Racing Job").SaveX(ctx).ID
				} else {
					id = client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(finished).SetName("Racing Project").SaveX(ctx).ID
				}
				path := fmt.Sprintf("/%ss/%d", kind, id)
				archive := creditCommand{path: path + "/delete"}
				reopen := creditCommand{path: path + "/status", values: currentJobFormValues(t, web, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(unfinished)}})}
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				var archived, reopened *httptest.ResponseRecorder
				if archiveFirst {
					archived, reopened = f.ordered(t, archive, reopen)
				} else {
					reopened, archived = f.ordered(t, reopen, archive)
				}
				var closed bool
				var status int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,status_id FROM `+kind+`s WHERE id=$1`, id).Scan(&closed, &status))
				if archiveFirst {
					if archived.Code != 303 || reopened.Code < 400 || !closed || status != finished {
						t.Fatalf("archive first: archive=%d reopen=%d archived=%v status=%d", archived.Code, reopened.Code, closed, status)
					}
				} else if reopened.Code != 303 || archived.Code != 409 || closed || status != unfinished {
					t.Fatalf("reopen first: archive=%d reopen=%d archived=%v status=%d", archived.Code, reopened.Code, closed, status)
				}
			})
		}
	}
}
