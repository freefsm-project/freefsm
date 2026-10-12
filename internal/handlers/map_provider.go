package handlers

import (
	"net/url"
)

// Credential-bearing legacy URLs are retained for review, never published to
// browsers or logs. Optional-provider loss must not disable the schedule.
func safeMapTileURL(raw string) string {
	u, err := url.Parse(mapTileURLSample(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return raw
}
