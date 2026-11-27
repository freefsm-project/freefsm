package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/html"
)

type discoveryFixture struct {
	pool           *pgxpool.Pool
	client         *ent.Client
	web            http.Handler
	sessions       *services.SessionService
	company, admin int64
	cookie         *http.Cookie
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()
	p, c, w, s, company, admin := permissionHTTPFixture(t)
	return &discoveryFixture{p, c, w, s, company, admin, sessionCookie(t, context.Background(), s, admin)}
}

func (f *discoveryFixture) reader(t *testing.T, name string, grants ...authorization.Grant) (int64, int64, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	svc := authorization.New(f.pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, 0, name, grants)
	historyCheck(t, err)
	u := f.client.User.Create().SetCompanyID(f.company).SetName(name).SetEmail(name + "@discovery.test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: f.admin}, u.ID, role))
	return u.ID, role, sessionCookie(t, ctx, f.sessions, u.ID)
}

type discoveryObservation struct {
	IDs     []string
	Text    []string
	Total   int
	Empty   string
	Choices []string
}

// These attributes describe domain identities, not template layout. Requiring
// them prevents a passing "no results" assertion caused by a broken selector.
func discoveryObserve(t *testing.T, body string) discoveryObservation {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	historyCheck(t, err)
	o := discoveryObservation{IDs: []string{}, Text: []string{}, Total: -1}
	var resultText string
	var text func(*html.Node) string
	text = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var out string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			out += text(c)
		}
		return out
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		attrs := map[string]string{}
		for _, a := range n.Attr {
			attrs[a.Key] = a.Val
		}
		if _, ok := attrs["data-discovery-results"]; ok {
			resultText = strings.Join(strings.Fields(text(n)), " ")
		}
		if n.Data == "input" && attrs["name"] == "type" {
			o.Choices = append(o.Choices, attrs["value"])
		}
		if _, ok := attrs["data-discovery-status"]; ok {
			raw := strings.TrimSuffix(strings.TrimSpace(text(n)), " results")
			total, err := strconv.Atoi(raw)
			historyCheck(t, err)
			o.Total = total
		}
		if typ, ok := attrs["data-record-type"]; ok {
			id, err := strconv.ParseInt(attrs["data-record-id"], 10, 64)
			historyCheck(t, err)
			o.IDs = append(o.IDs, fmt.Sprintf("%s:%d", typ, id))
			o.Text = append(o.Text, strings.Join(strings.Fields(text(n)), " "))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if o.Total < 0 {
		t.Fatalf("missing discovery total in response: %.1500s", body)
	}
	if o.Total == 0 {
		o.Empty = resultText
	}
	return o
}

func (f *discoveryFixture) search(t *testing.T, cookie *http.Cookie, query string) discoveryObservation {
	t.Helper()
	return discoveryObserve(t, requestBody(t, f.web, cookie, "GET", "/search?"+query, 200))
}

func discoveryEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}

func discoveryCall(web http.Handler, cookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	return w
}
