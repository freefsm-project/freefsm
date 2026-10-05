package config

import "testing"

func TestBackupBuildAdmission(t *testing.T) {
	oldKind, oldVersion, oldCommit, oldExact := BuildKind, Version, Commit, ExactRelease
	t.Cleanup(func() { BuildKind, Version, Commit, ExactRelease = oldKind, oldVersion, oldCommit, oldExact })
	for _, version := range []string{"dev", "", "v1.2.3-4-gabcdef-dirty"} {
		BuildKind, Version, Commit, ExactRelease = "development", version, "none", "false"
		if reason := BackupDisabledReason(); reason != "" {
			t.Fatalf("development disabled: %s", reason)
		}
	}
	BuildKind = "release"
	if BackupDisabledReason() == "" {
		t.Fatal("invalid declared release admitted as development")
	}
	BuildKind = "unknown"
	if BackupDisabledReason() == "" {
		t.Fatal("unknown build kind admitted")
	}
}
