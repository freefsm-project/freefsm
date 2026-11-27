package handlers

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestDiscoveryLiteralZeroItemNoMatchHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.client.Item.Create().SetCompanyID(f.company).SetName("Alphabetic item").SaveX(context.Background())
	for _, key := range []string{"q", "search"} {
		t.Run(key, func(t *testing.T) {
			body := requestBody(t, f.web, f.cookie, "GET", "/items?"+key+"=0", 200)
			if !strings.Contains(body, `data-list-empty="no-matches"`) || !regexp.MustCompile(`(?i)no match(?:es|ing)`).MatchString(body) {
				t.Fatal("literal zero must produce no-match wording")
			}
			if strings.Contains(strings.ToLower(body), "create your first") {
				t.Fatal("literal zero must not be treated as an absent filter")
			}
			if !strings.Contains(strings.ToLower(body), "clear filters") {
				t.Fatal("literal zero must offer filter recovery")
			}
		})
	}
}
