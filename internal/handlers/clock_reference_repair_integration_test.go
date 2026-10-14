package handlers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

// F58-2: use the real reference module against TCP HTTP, retaining the two
// independent reviewer scenarios and extending both to exact-entry out.
func TestClockReferenceGPSRepairHTTP(t *testing.T) {
	for _, kind := range []string{"in", "out"} {
		for _, mode := range []string{"invalid-timestamp", "removed-grant"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				svc := authorization.New(pool)
				user := client.User.Create().SetCompanyID(company).SetEmail("clock-repair@test").SetName("Clock worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.clock_out", Scope: authorization.Own}}
				role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Clock repair", append(append([]authorization.Grant{}, grants...), authorization.Grant{Permission: "time.gps.capture", Scope: authorization.Own}))
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
					t.Fatal(err)
				}
				token, err := sessions.CreateMobile(ctx, user.ID, "clock repair")
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
				defer server.Close()
				runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(runCtx, "node", "testdata/clock-gps-repair.mjs", server.URL, token, mode, kind)
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				var occurrence time.Time
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					t.Log(line)
					if strings.HasPrefix(line, "STAGED ") {
						occurrence, err = time.Parse(time.RFC3339Nano, strings.TrimPrefix(line, "STAGED "))
						if err == nil && mode == "removed-grant" {
							_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Clock repair", grants, 1)
						}
						if err != nil {
							cancel()
							_ = cmd.Wait()
							t.Fatal(err)
						}
						if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
							t.Error(err)
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Error(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("reference repair: %v\n%s", err, stderr.String())
				}
				entry := client.TimeEntry.Query().OnlyX(ctx)
				if entry.UserID != user.ID || kind == "in" && (!entry.ClockIn.Equal(occurrence) || entry.ClockOut != nil || entry.Notes != "original notes retained") || kind == "out" && (entry.ClockOut == nil || !entry.ClockOut.Equal(occurrence)) {
					t.Fatalf("original occurrence/subject/notes not retained: %+v", entry)
				}
				var gps int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_clock_gps`).Scan(&gps); err != nil {
					t.Fatal(err)
				}
				if gps != 0 {
					t.Fatal("location-free repair invented GPS")
				}
			})
		}
	}
}
