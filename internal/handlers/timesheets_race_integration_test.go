package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

func waitTimesheetReaders(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("report/writer did not reach real authority-lock contention")
}

func TestTimesheetsConcurrentWritersProduceOneSnapshotHTTP(t *testing.T) {
	for _, kind := range []string{"correction", "void", "clock-out"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
			builder := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetNotes("Coherent report row")
			if kind == "correction" {
				builder.SetClockOut(start.Add(time.Hour))
			}
			entry := builder.SaveX(ctx)
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			request := httptest.NewRequest("GET", "/time-entries?include_void=true", nil).WithContext(ctx)
			request.AddCookie(sessionCookie(t, ctx, sessions, admin))
			barrier, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			writer := make(chan error, 1)
			go func() {
				switch kind {
				case "correction":
					end := start.Add(90 * time.Minute)
					writer <- auth.CorrectTimeEntry(ctx, actor, entry.ID, authorization.TimeEntryCorrection{ClockOut: &end, Reason: "Concurrent report correction"})
				case "void":
					writer <- auth.VoidTimeEntry(ctx, actor, entry.ID, "Concurrent terminal report Void")
				case "clock-out":
					_, err := auth.Clock(ctx, actor, true, authorization.ClockCommand{EntryID: entry.ID})
					writer <- err
				}
			}()
			report := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); report <- w }()
			waitTimesheetReaders(t, ctx, pool, 2)
			if err := barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-writer; err != nil {
				t.Fatal(err)
			}
			response := <-report
			if response.Code == 503 {
				assertNotContains(t, response.Body.String(), "data-timesheet-total")
				return
			}
			if response.Code != 200 {
				t.Fatalf("report=%d %s", response.Code, response.Body)
			}
			body := response.Body.String()
			total := regexp.MustCompile(`<strong data-timesheet-total>([^<]+)</strong>`).FindStringSubmatch(body)
			row := regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-time-entry="%d">(.*?)</tr>`, entry.ID)).FindStringSubmatch(body)
			if len(total) != 2 || len(row) != 2 {
				t.Fatal("report omitted coherent row or total")
			}
			if strings.Contains(row[1], "Excluded (Void)") {
				if total[1] != "0s" {
					t.Fatalf("Void contributed %s", total[1])
				}
				assertContains(t, row[1], "No recorded end")
				assertNotContains(t, row[1], "Provisional")
			} else {
				duration := regexp.MustCompile(`<strong>([^<]+)</strong>`).FindStringSubmatch(row[1])
				if len(duration) != 2 || duration[1] != total[1] {
					t.Fatalf("mixed rows/total: row=%v total=%v", duration, total)
				}
				if kind == "correction" && total[1] != "1h0m0s" && total[1] != "1h30m0s" {
					t.Fatalf("mixed correction %s", total[1])
				}
			}
		})
	}
}

func TestTimesheetsRecheckAuthorityAfterSnapshotWaitHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := authorization.New(pool)
	a := authorization.Actor{UserID: admin}
	worker := client.User.Create().SetCompanyID(company).SetEmail("report-revocation@test").SetName("Report subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := auth.SaveRole(ctx, a, 0, "Report before revocation", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := auth.SaveRole(ctx, a, 0, "Report after revocation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, a, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(worker.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Revoked report secret").SaveX(ctx)
	request := httptest.NewRequest("GET", "/time-entries", nil).WithContext(ctx)
	request.AddCookie(sessionCookie(t, ctx, sessions, worker.ID))
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() { writer <- auth.AssignUser(ctx, a, worker.ID, empty) }()
	waitTimesheetReaders(t, ctx, pool, 1)
	report := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); report <- w }()
	waitTimesheetReaders(t, ctx, pool, 2)
	if err := barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	response := <-report
	if response.Code != 403 {
		t.Fatalf("snapshot waiter retained revoked authority: %d %s", response.Code, response.Body)
	}
	assertNotContains(t, response.Body.String(), "Revoked report secret")
	assertNotContains(t, response.Body.String(), "data-timesheet-total")
}
