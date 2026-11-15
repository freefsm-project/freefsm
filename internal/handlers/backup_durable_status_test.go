package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

// Real Manager persistence and production router, with deliberately unavailable
// authentication dependencies. No synthetic admission owner or installed DB proof.
func TestBackupDurableStatusRouterWithoutAuthentication(t *testing.T) {
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := backup.Config{Control: c, StateDir: c.StateDir(), UploadDir: filepath.Join(root, "private-uploads"), DSN: "private-dsn", BuildKind: backup.BuildDevelopment,
		ActivitySink: func(context.Context, backup.ActivityEvent) error { return errors.New("private-sink-error") }}
	m, err := backup.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a3", 32)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.StageUploadAction(ctx, backup.ActivityActor{ID: 17, Name: "private-actor"}, id, strings.NewReader("private-archive"), "private-password"); err == nil {
		t.Fatal("cancelled upload succeeded")
	}
	m.Shutdown()
	c, err = instancecontrol.New(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Control = c
	m, err = backup.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if _, err := m.Status(18, id); !errors.Is(err, backup.ErrNotFound) {
		t.Fatal("wrong actor authorized", err)
	}
	if _, _, _, err := m.OpenDownload(17, id); err == nil {
		t.Fatal("reconstructed download authority")
	}
	if _, err := m.StartRestore(context.Background(), 17, id, "destination", "destination"); err == nil {
		t.Fatal("reconstructed restore authority")
	}
	router := NewBackupRouter(nil, m, c, nil, nil, nil, "", nil)
	shell := httptest.NewRecorder()
	router.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/settings/backup", nil))
	if shell.Code != 200 {
		t.Fatal(shell.Code, shell.Body.String())
	}
	for _, forbidden := range []string{"<form", "<input", `id="backup-activity"`, "private-"} {
		if strings.Contains(shell.Body.String(), forbidden) {
			t.Fatal("private shell", forbidden)
		}
	}
	match := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(shell.Body.String())
	if len(match) != 2 {
		t.Fatal("missing CSRF shell token")
	}
	post := func(path, header, capability string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/settings/backup/"+path, nil)
		r.Header.Set("X-CSRF-Token", match[1])
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set(header, capability)
		for _, cookie := range shell.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := post("status", "X-Backup-Operation", id)
	var op backup.Operation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil || !op.Reconstructed || op.Phase != "failed" || !op.ActivityPending || len(op.Diagnostics) != 2 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	for _, secret := range []string{"private-", root, "ArchiveDigest\":\"private", "SourceName\":\"private"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("status disclosed private data")
		}
	}
	w = post("action-status", "X-Backup-Action", id)
	var receipt backup.InitiationReceipt
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || !receipt.StatusAvailable || receipt.Available || receipt.Phase != op.Phase {
		t.Fatalf("receipt %d %s", w.Code, w.Body)
	}
	digest := sha256.Sum256([]byte("status\x00" + id))
	for _, wrong := range []string{strings.Repeat("b4", 32), hex.EncodeToString(digest[:])} {
		if w := post("status", "X-Backup-Operation", wrong); w.Code != 404 {
			t.Fatalf("wrong capability: %d", w.Code)
		}
	}
	for _, path := range []string{"create", "restore", "download", "review"} {
		if w := post(path, "X-Backup-Operation", id); w.Code != 503 {
			t.Fatalf("maintenance action %s: %d", path, w.Code)
		}
	}
}
