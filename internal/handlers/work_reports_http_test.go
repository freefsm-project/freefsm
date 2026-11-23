package handlers

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkloadFiltersBoundariesAndDrilldownsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`)
	historyCheck(t, err)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Workload selected Customer").SaveX(ctx)
	loc, err := time.LoadLocation("America/New_York")
	historyCheck(t, err)
	for _, day := range []string{"2026-03-08", "2026-11-01"} {
		t.Run(day, func(t *testing.T) {
			start, err := time.ParseInLocation("2006-01-02", day, loc)
			historyCheck(t, err)
			end := start.AddDate(0, 0, 1)
			cross := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Cross midnight " + day).SetStartTime(start.Add(-time.Hour)).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
			client.JobAssignment.Create().SetJobID(cross.ID).SetUserID(admin).SaveX(ctx)
			second := client.User.Create().SetCompanyID(company).SetName("Retained assignee " + day).SetEmail(day + "@workload.test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(cross.ID).SetUserID(second.ID).SaveX(ctx)
			client.User.UpdateOneID(second.ID).SetIsActive(false).SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Ends at boundary " + day).SetStartTime(start.Add(-2 * time.Hour)).SetEndTime(start).SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Starts at boundary " + day).SetStartTime(end).SetEndTime(end.Add(time.Hour)).SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Full local day " + day).SetStartTime(start).SetEndTime(end).SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unscheduled " + day).SaveX(ctx)
			q := url.Values{"scope": {"A"}, "schedule_from": {day}, "schedule_to": {day}, "customer_id": {fmt.Sprint(c.ID)}}
			path := "/reports/workload?" + q.Encode()
			body := requestBody(t, web, cookie, "GET", path, 200)
			for _, want := range []string{"Cross midnight " + day, "Full local day " + day, "Unscheduled " + day, "Unassigned", second.Name} {
				assertContains(t, body, want)
			}
			for _, absent := range []string{"Ends at boundary " + day, "Starts at boundary " + day} {
				assertNotContains(t, body, absent)
			}
			assertContains(t, strings.ToLower(body), "nonadditive")
			wantTotal := 3
			if day == "2026-11-01" {
				wantTotal = 4
			} // both retained unscheduled Jobs
			workReportTotal(t, body, "Distinct Jobs", wantTotal)
			workReportTotal(t, body, "Scheduled", 2)
			workReportTotal(t, body, "Completed", 0)
			// Follow the actual rendered measure link, rather than fabricating a
			// second selector in the test. Every selected filter must survive.
			found := false
			for _, match := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
				u, err := url.Parse(html.UnescapeString(match[1]))
				historyCheck(t, err)
				if u.Path != "/reports/workload" || u.Query().Get("measure") != "scheduled" {
					continue
				}
				if u.Query().Get("customer_id") != fmt.Sprint(c.ID) || u.Query().Get("schedule_from") != day || u.Query().Get("schedule_to") != day {
					t.Fatalf("drilldown lost selected filters: %s", u)
				}
				detail := requestBody(t, web, cookie, "GET", u.String(), 200)
				assertContains(t, detail, "Cross midnight "+day)
				assertNotContains(t, detail, "Unscheduled "+day)
				found = true
				break
			}
			if !found {
				t.Fatal("scheduled summary has no matching drilldown")
			}
			q.Set("measure", "completed")
			zero := requestBody(t, web, cookie, "GET", "/reports/workload?"+q.Encode(), 200)
			assertNotContains(t, zero, "Cross midnight "+day)
			assertContains(t, zero, "No matching records for these filters.")
			assertNotContains(t, zero, "Report unavailable")
		})
	}
}

func workReportTotal(t *testing.T, body, label string, want int) {
	t.Helper()
	pattern := `<dt>` + regexp.QuoteMeta(label) + `</dt>\s*<dd>\s*<a[^>]*>([0-9]+)</a>`
	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing report measure %q", label)
	}
	got, err := strconv.Atoi(match[1])
	historyCheck(t, err)
	if got != want {
		t.Fatalf("%s=%d want %d", label, got, want)
	}
}

func workReportMeasureLink(t *testing.T, body, label string) string {
	t.Helper()
	match := regexp.MustCompile(`<dt>` + regexp.QuoteMeta(label) + `</dt>\s*<dd>\s*<a[^>]*href="([^"]+)"[^>]*>`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing linked measure %q", label)
	}
	return html.UnescapeString(match[1])
}

func TestWorkloadConjunctiveMeasureAndAssigneeDrilldownHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Intersection Customer").SaveX(ctx)
	var yesterday time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT current_date-1`).Scan(&yesterday))
	start := time.Now().Add(-time.Hour)
	for _, scenario := range []struct {
		title                        string
		scheduled, overdue, assigned bool
	}{
		{"Intersection selected", true, true, true},
		{"Scheduled but not overdue", true, false, true},
		{"Overdue but not scheduled", false, true, true},
		{"Other assignee intersection", true, true, false},
	} {
		j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType(scenario.title)
		if scenario.scheduled {
			j.SetStartTime(start).SetEndTime(start.Add(time.Hour))
		}
		if scenario.overdue {
			j.SetDueDate(yesterday)
		}
		job := j.SaveX(ctx)
		if scenario.assigned {
			client.JobAssignment.Create().SetJobID(job.ID).SetUserID(admin).SaveX(ctx)
		}
	}
	path := fmt.Sprintf("/reports/workload?scope=A&measure=overdue&assignee_id=%d&customer_id=%d", admin, c.ID)
	body := requestBody(t, web, cookie, "GET", path, 200)
	workReportTotal(t, body, "Distinct Jobs", 2)
	workReportTotal(t, body, "Scheduled", 1)
	link := workReportMeasureLink(t, body, "Scheduled")
	detail := requestBody(t, web, cookie, "GET", link, 200)
	workReportTotal(t, detail, "Distinct Jobs", 1)
	assertContains(t, detail, "Intersection selected")
	for _, absent := range []string{"Scheduled but not overdue", "Overdue but not scheduled", "Other assignee intersection"} {
		assertNotContains(t, detail, absent)
	}
	// Group summaries must also retain the currently selected measure.
	sections := regexp.MustCompile(`(?s)<section>(.*?)</section>`).FindAllStringSubmatch(body, -1)
	if len(sections) == 0 {
		t.Fatal("missing assignee group")
	}
	groupLink := workReportMeasureLink(t, sections[0][1], "Scheduled")
	group := requestBody(t, web, cookie, "GET", groupLink, 200)
	workReportTotal(t, group, "Distinct Jobs", 1)
	assertContains(t, group, "Intersection selected")
}

func TestWorkloadCompanyDateOverdueAndCategoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='Pacific/Auckland'`)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT (current_timestamp AT TIME ZONE 'Pacific/Auckland')::date`).Scan(&today))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Due date Customer").SaveX(ctx)
	for _, scenario := range []struct {
		title, category string
		due             time.Time
		archived        bool
	}{
		{"Unscheduled overdue", "job:new", today.AddDate(0, 0, -1), false},
		{"Due today", "job:new", today, false},
		{"Completed yesterday", "job:completed", today.AddDate(0, 0, -1), false},
		{"Canceled yesterday", "job:canceled", today.AddDate(0, 0, -1), false},
		{"Archived overdue", "job:new", today.AddDate(0, 0, -1), true},
	} {
		var status int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key=$1 LIMIT 1`, scenario.category).Scan(&status))
		j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType(scenario.title).SetStatusID(status).SetDueDate(scenario.due)
		if scenario.archived {
			j.SetDeletedAt(time.Now())
		}
		j.SaveX(ctx)
	}
	body := requestBody(t, web, cookie, "GET", "/reports/workload?scope=A", 200)
	workReportTotal(t, body, "Distinct Jobs", 4)
	workReportTotal(t, body, "Unscheduled", 4)
	workReportTotal(t, body, "Overdue", 1)
	workReportTotal(t, body, "Completed", 1)
	assertNotContains(t, body, "Archived overdue")
	body = requestBody(t, web, cookie, "GET", "/reports/workload?scope=A&measure=overdue", 200)
	assertContains(t, body, "Unscheduled overdue")
	for _, absent := range []string{"Due today", "Completed yesterday", "Canceled yesterday", "Archived overdue"} {
		assertNotContains(t, body, absent)
	}
	// Category keys, not editable status labels, define the measure.
	_, err = pool.Exec(ctx, `UPDATE statuses SET name='Renamed completed label' WHERE category_key='job:completed'`)
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/reports/workload?scope=A&category=job%3Acompleted", 200)
	workReportTotal(t, body, "Completed", 1)
	workReportTotal(t, body, "Overdue", 0)
	assertContains(t, body, "Completed yesterday")
}
