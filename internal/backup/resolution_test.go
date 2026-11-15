package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

// Real filesystem and real owning locks; no database or fabricated engine proof.
func resolutionFixture(t *testing.T) (*Manager, resolutionRecord) {
	t.Helper()
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{DSN: "resolution-test-destination", UploadDir: filepath.Join(root, "uploads"), StateDir: c.StateDir(), Control: c, BuildKind: BuildDevelopment})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := c.OperationLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	release, err := c.Maintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	j := journal{Phase: "committed", Recovery: strings.Repeat("ab", 32) + "-recovery", Destination: m.destinationIdentity(), UploadDir: m.cfg.UploadDir}
	b, err := m.journalResolution(j, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	return m, resolutionRecord{Version: 1, Binding: b, Stage: resolutionSelected}
}

func requireResolutionError(t *testing.T, err error, kind string) {
	t.Helper()
	var e *resolutionError
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("want %s, got %v", kind, err)
	}
}

func resolutionBytes(t *testing.T, r resolutionRecord) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestResolutionStagesAndRetainedIdentity(t *testing.T) {
	m, r := resolutionFixture(t)
	_, err := m.readResolution(r.Binding, r.Stage)
	requireResolutionError(t, err, "missing")
	for _, stage := range []resolutionStage{resolutionSelected, resolutionResolved, resolutionReady} {
		r.Stage = stage
		if err := m.persistResolution(r); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(m.resolutionPath(r.Binding))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.persistResolution(r); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(m.resolutionPath(r.Binding))
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("idempotent retry rewrote evidence", err)
		}
		// Reconstruct the owner; status identity survives process-local job loss.
		fresh := &Manager{root: m.root, cfg: m.cfg}
		got, err := fresh.readResolution(r.Binding, stage)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("read: %#v %v", got, err)
		}
	}
	data, err := os.ReadFile(m.resolutionPath(r.Binding))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(strings.Repeat("ab", 32))) || bytes.Contains(data, []byte(m.cfg.DSN)) || bytes.Contains(data, []byte(m.cfg.UploadDir)) {
		t.Fatal("record exposes capability or configuration")
	}
	if _, _, err := m.cfg.Control.Enter(context.Background()); err == nil {
		t.Fatal("record alone opened admission")
	}
	// A later transition is separate retained history, not an overwrite authority.
	next := r
	next.Binding.Transition = strings.Repeat("ef", 32)
	next.Stage = resolutionSelected
	if err := m.persistResolution(next); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readResolution(r.Binding, resolutionReady); err != nil {
		t.Fatal(err)
	}
}

func TestResolutionInvalidAdvancementAndDiagnostics(t *testing.T) {
	m, r := resolutionFixture(t)
	r.Stage = resolutionReady
	requireResolutionError(t, m.persistResolution(r), "stage")
	r.Stage = resolutionSelected
	r.Diagnostics = []resolutionDiagnostic{{Category: FailureStorage, ID: strings.Repeat("12", 16)}}
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	next := r
	next.Stage = resolutionReady
	requireResolutionError(t, m.persistResolution(next), "stage")
	next = r
	next.Diagnostics = nil
	requireResolutionError(t, m.persistResolution(next), "mismatch")
	next.Diagnostics = []resolutionDiagnostic{{Category: FailureInternal, ID: strings.Repeat("12", 16)}}
	requireResolutionError(t, m.persistResolution(next), "mismatch")
	r.Diagnostics = append(r.Diagnostics, resolutionDiagnostic{Category: FailureRecoveryRequired, ID: strings.Repeat("34", 16)})
	r.Stage = resolutionResolved
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	next = r
	next.Stage = resolutionSelected
	requireResolutionError(t, m.persistResolution(next), "stage")
	r.Stage = resolutionReady
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	got, err := m.readResolution(r.Binding, r.Stage)
	if err != nil || !reflect.DeepEqual(got.Diagnostics, r.Diagnostics) {
		t.Fatal("diagnostic history lost", err)
	}
}

func TestResolutionPrepublicationFailuresRetainEvidence(t *testing.T) {
	for _, point := range []string{"resolution-create", "resolution-write", "resolution-file-sync", "resolution-rename"} {
		t.Run(point, func(t *testing.T) {
			m, r := resolutionFixture(t)
			if err := m.persistResolution(r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(m.resolutionPath(r.Binding))
			if err != nil {
				t.Fatal(err)
			}
			m.checkpoint = func(p string) error {
				if p == point {
					return syscall.EIO
				}
				return nil
			}
			r.Stage = resolutionResolved
			requireResolutionError(t, m.persistResolution(r), "persistence")
			after, err := os.ReadFile(m.resolutionPath(r.Binding))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("old evidence changed", err)
			}
			files, err := filepath.Glob(filepath.Join(m.root, ".resolution-*"))
			if err != nil || len(files) != 0 {
				t.Fatal("temporary files leaked", files, err)
			}
			m.checkpoint = nil
			if err := m.persistResolution(r); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("directory-unavailable", func(t *testing.T) {
		m, r := resolutionFixture(t)
		if err := os.Remove(m.root); err != nil {
			t.Fatal(err)
		}
		requireResolutionError(t, m.persistResolution(r), "persistence")
	})
}

func TestResolutionAmbiguousPublicationAndRepeatedReadFailure(t *testing.T) {
	m, r := resolutionFixture(t)
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	r.Stage = resolutionResolved
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	r.Stage = resolutionReady
	m.checkpoint = func(p string) error {
		if p == "resolution-directory-sync" {
			return syscall.EIO
		}
		return nil
	}
	requireResolutionError(t, m.persistResolution(r), "unresolved")
	// Rename happened; this is not success, nor a guaranteed restoration of old bytes.
	data, err := os.ReadFile(m.resolutionPath(r.Binding))
	if err != nil || !bytes.Equal(data, resolutionBytes(t, r)) {
		t.Fatal("expected published candidate", err)
	}
	for _, point := range []string{"resolution-read-file-sync", "resolution-read-directory-sync"} {
		fresh := &Manager{root: m.root, cfg: m.cfg, checkpoint: func(p string) error {
			if p == point {
				return syscall.EIO
			}
			return nil
		}}
		for i := 0; i < 3; i++ {
			got, err := fresh.readResolution(r.Binding, r.Stage)
			requireResolutionError(t, err, "persistence")
			if got.Version != 0 {
				t.Fatal("failed read returned usable record")
			}
			requireResolutionError(t, fresh.persistResolution(r), "persistence")
		}
		fresh.checkpoint = nil
		if err := fresh.persistResolution(r); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.readResolution(r.Binding, r.Stage); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolutionStrictDecodeAndPreservation(t *testing.T) {
	m, r := resolutionFixture(t)
	valid := string(resolutionBytes(t, r))
	cases := map[string]struct{ data, kind string }{
		"truncated":       {valid[:len(valid)-1], "corrupt"},
		"unknown":         {strings.Replace(valid, `"Version":1`, `"Version":1,"Extra":true`, 1), "corrupt"},
		"duplicate":       {strings.Replace(valid, `"Version":1`, `"Version":1,"Version":1`, 1), "corrupt"},
		"alias":           {strings.Replace(valid, `"Version":1`, `"version":1`, 1), "corrupt"},
		"trailing":        {valid + valid, "corrupt"},
		"unsupported":     {strings.Replace(valid, `"Version":1`, `"Version":2`, 1), "unsupported"},
		"missing-version": {strings.Replace(valid, `"Version":1,`, "", 1), "unsupported"},
		"invalid-stage":   {strings.Replace(valid, `"selected"`, `"complete"`, 1), "stage"},
		"null-binding":    {`{"Version":1,"Binding":null,"Stage":"selected","Diagnostics":null}`, "corrupt"},
		"oversize":        {strings.Repeat(" ", resolutionLimit+1), "corrupt"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(m.resolutionPath(r.Binding), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := m.readResolution(r.Binding, r.Stage)
			requireResolutionError(t, err, tc.kind)
			requireResolutionError(t, m.persistResolution(r), tc.kind)
			got, err := os.ReadFile(m.resolutionPath(r.Binding))
			if err != nil || string(got) != tc.data {
				t.Fatal("invalid evidence overwritten", err)
			}
		})
	}
}

func TestResolutionExactBinding(t *testing.T) {
	m, r := resolutionFixture(t)
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Operation", "Transition", "Destination", "UploadRoot", "Generation"} {
		t.Run(field, func(t *testing.T) {
			want := r.Binding
			reflect.ValueOf(&want).Elem().FieldByName(field).SetString(strings.Repeat("99", 32))
			if field == "Transition" {
				// An old record copied under a newer transition still cannot match.
				if err := os.WriteFile(m.resolutionPath(want), resolutionBytes(t, r), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := m.readResolution(want, r.Stage)
			requireResolutionError(t, err, "mismatch")
			next := r
			next.Binding = want
			requireResolutionError(t, m.persistResolution(next), "mismatch")
		})
	}
	want := r.Binding
	want.JournalPhase, want.Outcome = "rolled-back", "rollback"
	_, err := m.readResolution(want, r.Stage)
	requireResolutionError(t, err, "mismatch")
	_, err = m.readResolution(r.Binding, resolutionReady)
	requireResolutionError(t, err, "stage")
}

func TestResolutionExactInodeAndContent(t *testing.T) {
	for _, replacement := range []string{"inode", "content", "directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			m, r := resolutionFixture(t)
			if err := m.persistResolution(r); err != nil {
				t.Fatal(err)
			}
			m.checkpoint = func(p string) error {
				if p != "resolution-read-current" {
					return nil
				}
				path := m.resolutionPath(r.Binding)
				switch replacement {
				case "inode", "symlink":
					if err := os.Rename(path, path+".old"); err != nil {
						return err
					}
					if replacement == "symlink" {
						return os.Symlink(path+".old", path)
					}
					return os.WriteFile(path, resolutionBytes(t, r), 0600)
				case "content":
					r.Stage = resolutionResolved
					return os.WriteFile(path, resolutionBytes(t, r), 0600)
				case "directory":
					if err := os.Rename(m.root, m.root+"-old"); err != nil {
						return err
					}
					if err := os.Mkdir(m.root, 0700); err != nil {
						return err
					}
					return os.Link(filepath.Join(m.root+"-old", filepath.Base(path)), path)
				}
				return nil
			}
			_, err := m.readResolution(r.Binding, r.Stage)
			requireResolutionError(t, err, "mismatch")
		})
	}
}

func TestResolutionJournalAndBootstrapKinds(t *testing.T) {
	m, r := resolutionFixture(t)
	for phase, outcome := range map[string]string{"capturing": "no-replacement", "committed": "restore", "rolled-back": "rollback"} {
		j := journal{Phase: phase, Recovery: strings.Repeat("ab", 32) + "-recovery", Destination: m.destinationIdentity(), UploadDir: m.cfg.UploadDir}
		b, err := m.journalResolution(j, r.Binding.Generation)
		if err != nil || b.Outcome != outcome || b.Transition != r.Binding.Transition || b.Operation != r.Binding.Operation {
			t.Fatal("lost original journal identity", b, err)
		}
		j.Phase = "replacing"
		if _, err := m.journalResolution(j, r.Binding.Generation); err == nil {
			t.Fatal("unresolved journal accepted")
		}
		j.Phase, j.Destination = phase, "wrong"
		if _, err := m.journalResolution(j, r.Binding.Generation); err == nil {
			t.Fatal("wrong destination accepted")
		}
	}
	r.Binding.Kind, r.Binding.Operation, r.Binding.JournalPhase = "bootstrap", "", ""
	r.Binding.Outcome = "no-replacement"
	if err := m.persistResolution(r); err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.Binding.Operation = strings.Repeat("ab", 32)
	requireResolutionError(t, m.persistResolution(bad), "corrupt")
	bad = r
	bad.Binding.Outcome = "restore"
	requireResolutionError(t, m.persistResolution(bad), "corrupt")
}
