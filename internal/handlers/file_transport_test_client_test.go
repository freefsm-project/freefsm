package handlers

import (
	"github.com/google/uuid"
	"net/http"
	"strings"
)

// Existing protocol fixtures stage a deterministic test-client operation per
// exact request, independently of resource/publication IDs. Production clients
// stage random UUIDs (reference.js); explicit identity-misuse tests set their own.
func stageTestFileTransport(r *http.Request, body string) {
	if r.Method == "POST" && r.URL.Path == "/file-transfers" || r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/file-transfers/") && strings.HasSuffix(r.URL.Path, "/chunks") {
		r.Header.Set("Operation-ID", uuid.NewSHA1(uuid.NameSpaceURL, []byte(r.Header.Get("Authorization")+"\n"+r.Method+"\n"+r.URL.String()+"\n"+body)).String())
	}
}
