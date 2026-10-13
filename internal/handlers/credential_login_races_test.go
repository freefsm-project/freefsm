package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"golang.org/x/crypto/bcrypt"
)

func TestCredentialLoginRacesThroughBrowserAndAPIHTTP(t *testing.T) {
	for _, channel := range []string{"web", "api"} {
		for _, change := range []string{"reset", "disable", "force"} {
			t.Run(channel+"/"+change, func(t *testing.T) {
				pool, client, router, sessions, company, _ := permissionHTTPFixture(t)
				ctx := context.Background()
				hash, err := bcrypt.GenerateFromPassword([]byte("BeforeRace1!"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				u := client.User.Create().SetCompanyID(company).SetEmail("login-race@test").SetName("Login race").SetRole("tech").SetPasswordHash(string(hash)).SaveX(ctx)
				api := apiv1.NewRouter(pool, client, sessions)
				var pairingBody string
				if channel == "api" {
					pairingBody = approvedPairing(t, api, router, sessionCookie(t, ctx, sessions, u.ID))
				}
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
					t.Fatal(err)
				}
				var pid int
				if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					var r *http.Request
					if channel == "api" {
						r = httptest.NewRequest("POST", "/pairings/redeem", strings.NewReader(pairingBody))
						r.Header.Set("Content-Type", "application/json")
					} else {
						r = httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"email": {"login-race@test"}, "password": {"BeforeRace1!"}}.Encode()))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					w := httptest.NewRecorder()
					if channel == "api" {
						api.ServeHTTP(w, r)
					} else {
						router.ServeHTTP(w, r)
					}
					done <- w
				}()
				waitCredentialBlock(t, pool, pid)
				replacement, e := bcrypt.GenerateFromPassword([]byte("ReplacementRace1!"), bcrypt.MinCost)
				if e != nil {
					t.Fatal(e)
				}
				switch change {
				case "reset":
					_, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,force_password_change=true WHERE id=$1`, u.ID, string(replacement))
				case "disable":
					_, err = tx.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, u.ID)
				case "force":
					_, err = tx.Exec(ctx, `UPDATE users SET force_password_change=true WHERE id=$1`, u.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				w := <-done
				var n int
				kind := "web"
				if channel == "api" {
					kind = "mobile"
				}
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND kind=$2`, u.ID, kind).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if channel == "web" && change == "force" {
					// Browser credentials remain valid, but the resulting session is confined to
					// forced replacement; the API never receives a usable bearer in this state.
					if n != 1 {
						t.Fatalf("forced web sessions=%d", n)
					}
					var cookie *http.Cookie
					for _, c := range w.Result().Cookies() {
						if c.Name == "session" {
							cookie = c
						}
					}
					if cookie == nil {
						t.Fatal("no replacement browser")
					}
					expectStatus(t, router, cookie, "GET", "/jobs", 303)
				} else if n != 0 || channel == "api" && w.Code != 409 {
					t.Fatalf("stale login status=%d sessions=%d body=%s", w.Code, n, w.Body.String())
				}
			})
		}
	}
}

func TestAPILoginThenAdministratorResetRevokesActualBearer(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("MobileBefore1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("mobile-real@test").SetName("Mobile").SetRole("tech").SetPasswordHash(string(hash)).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	w := pairingCall(api, "/pairings/redeem", approvedPairing(t, api, router, sessionCookie(t, ctx, sessions, u.ID)))
	if w.Code != 201 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/users/%d/reset-password", u.ID), url.Values{"password": {"TemporaryMobile1!"}, "confirm_password": {"TemporaryMobile1!"}}, 303)
	req := httptest.NewRequest("GET", "/me", nil)
	if result.Data.Token == "" {
		t.Fatal("missing actual bearer")
	}
	req.Header.Set("Authorization", "Bearer "+result.Data.Token)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatalf("post-reset bearer status=%d", response.Code)
	}
}
