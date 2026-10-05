package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSanitizedDiagnosticsRetainActionableCauses(t *testing.T) {
	secret := "PAYLOAD_PASSWORD_postgres://user:password@secret-host/private"
	err := classified(FailureRecoveryRequired, errors.Join(
		&pgconn.PgError{Code: "23514", Message: secret, Detail: secret, Where: "COPY probe, line 1: " + secret, InternalQuery: secret},
		&os.PathError{Op: "open", Path: secret, Err: syscall.ENOSPC},
	))
	d := buildDiagnostic(err, "rolling-back")
	if d.Failure.Category != FailureRecoveryRequired || len(d.Causes) != 2 || d.Causes[0].SQLState != "23514" || d.Causes[1].Category != FailureCapacity {
		t.Fatalf("lost diagnostic categories: %+v", d)
	}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), secret) || len(b) > 8192 {
		t.Fatalf("unsafe diagnostic: %s", b)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("raw error exposed")
	}
	for _, tc := range []struct {
		err  error
		want FailureCategory
	}{
		{engineFailure(FailureReleaseMismatch, "release mismatch"), FailureReleaseMismatch},
		{engineFailure(FailureSchemaMismatch, "uploaded schema differs from trusted destination schema"), FailureSchemaMismatch},
		{engineFailure(FailureFileReferences, "database references missing or unsafe file"), FailureFileReferences},
		{engineFailure(FailureToolCompatibility, "PostgreSQL tools must match server major version"), FailureToolCompatibility},
		{engineFailure(FailureReleaseMismatch, "explanation revised without changing the category"), FailureReleaseMismatch},
		{errors.New("release mismatch"), FailureInternal},
		{&pgconn.PgError{Code: "42501", Message: secret}, FailureDatabasePrivileges},
		{context.DeadlineExceeded, FailureCancelled},
		{errors.New(secret), FailureInternal},
	} {
		if got := buildDiagnostic(tc.err, "preflight"); got.Failure.Category != tc.want {
			t.Fatalf("category %s, want %s", got.Failure.Category, tc.want)
		}
	}
}

func TestInternalFailureProducersCarryCategories(t *testing.T) {
	assertCategory := func(err error, want FailureCategory) {
		t.Helper()
		if err == nil {
			t.Fatal("expected failure")
		}
		var typed *engineError
		if !errors.As(err, &typed) {
			t.Fatalf("internal producer returned untyped error: %T", err)
		}
		d := buildDiagnostic(err, "preflight")
		if d.Failure.Category != want || len(d.Causes) != 1 || d.Causes[0].Category != want {
			t.Fatalf("producer diagnostic=%+v want=%s", d, want)
		}
	}
	_, err := readLine(bufio.NewReader(strings.NewReader("incomplete SQL")))
	assertCategory(err, FailureArchiveFormat)
	_, err = decodeCopyPath(`invalid\q`)
	assertCategory(err, FailureFileReferences)
	w := &boundedWriter{w: io.Discard, remaining: 1}
	_, err = w.Write([]byte("too long"))
	assertCategory(err, FailureResourceLimit)
	assertCategory(space(t.TempDir(), -1), FailureCapacity)
	p := filepath.Join(t.TempDir(), "data.sql")
	if err = os.WriteFile(p, []byte("SELECT unsafe();\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertCategory(importData(context.Background(), nil, p), FailureArchiveFormat)
}

func TestCommandDiagnosticsNeverReturnRawStderr(t *testing.T) {
	m := storageManager(t)
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' 'pg_restore: error: could not execute query: ERROR:  permission denied for schema SECRET_SCHEMA' 'DETAIL: Failing row contains (SECRET_PASSWORD).' 'CONTEXT: COPY files, line 1: "postgres://SECRET_DSN"' 'Command was: INSERT INTO secrets VALUES (SECRET_PAYLOAD);' >&2
exit 7
`
	if e := os.WriteFile(filepath.Join(dir, "pg_restore"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	err := m.command(context.Background(), "pg_restore", io.Discard, "--file=-", "unused")
	if err == nil {
		t.Fatal("subprocess failure lost")
	}
	d := buildDiagnostic(err, "replacing-database")
	if d.Failure.Category != FailureDatabasePrivileges || len(d.Causes) != 1 || d.Causes[0].Tool != "pg_restore" || d.Causes[0].ExitCode != 7 {
		t.Fatalf("diagnostic: %+v", d)
	}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("stderr leaked: %s", b)
	}
	var reported Diagnostic
	m.cfg.ReportFailure = func(d Diagnostic) { reported = d }
	failure := m.reportFailure(err, "replacing-database")
	stored, e := os.ReadFile(filepath.Join(m.root, "last-failure.json"))
	if e != nil {
		t.Fatal(e)
	}
	if failure.DiagnosticID == "" || reported.Failure != failure || strings.Contains(string(stored), "SECRET") {
		t.Fatal("unsafe or uncorrelated persisted report")
	}
}

func TestStderrDiagnosticResourceBounds(t *testing.T) {
	s := &stderrSignals{}
	line := []byte(strings.Repeat("SECRET", 1024))
	for i := 0; i < 1000; i++ {
		n, e := s.Write(line)
		if n != len(line) || e != nil {
			t.Fatal("stderr must always drain")
		}
	}
	_, _ = s.Write([]byte("\npg_restore: error: permission denied SECRET\n"))
	if len(s.line) > stderrLineBudget || s.seen > stderrBudget || !s.truncated || len(s.signals) != 0 {
		t.Fatalf("unbounded stderr collector: %d/%d", len(s.line), s.seen)
	}
	d := buildDiagnostic(commandDiagnostic("pg_restore", errors.New("SECRET"), s), "validating")
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	if len(b) > 8192 || strings.Contains(string(b), "SECRET") {
		t.Fatal("raw stderr retained")
	}
}

func TestStartRestoreRejectsAnyJournalEntryBeforeMutatingJob(t *testing.T) {
	for _, kind := range []string{"file", "directory", "dangling-link", "unreadable-path"} {
		t.Run(kind, func(t *testing.T) {
			m := storageManager(t)
			m.cfg.ResetConnections = func() {}
			j, unlock, e := m.newJob(7, "upload")
			if e != nil {
				t.Fatal(e)
			}
			j.active = false
			j.Phase = "ready"
			unlock()
			m.wg.Done()
			defer m.Shutdown()
			before := j.Operation
			switch kind {
			case "file":
				e = os.WriteFile(m.journalPath(), []byte(`{"Phase":"replacing"}`), 0600)
			case "directory":
				e = os.Mkdir(m.journalPath(), 0700)
			case "dangling-link":
				e = os.Symlink("missing-target", m.journalPath())
			case "unreadable-path":
				m.root = filepath.Join(m.root, "not-a-directory")
				e = os.WriteFile(m.root, []byte("not a directory"), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = m.StartRestore(context.Background(), 7, j.ID, "destination", "destination"); !errors.Is(e, ErrRecoveryRequired) {
				t.Fatalf("guard: %v", e)
			}
			if j.Operation != before || j.active {
				t.Fatal("rejected restore consumed staged operation")
			}
			release, e := m.cfg.Control.OperationLock()
			if e != nil {
				t.Fatal("rejected restore leaked lock")
			}
			release()
		})
	}
}
