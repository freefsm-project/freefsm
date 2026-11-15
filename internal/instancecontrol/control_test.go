package instancecontrol_test

import (
	"errors"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

// Removing a marker requires write/search permission, but opening its directory
// for durable synchronization also requires read permission. A failed enable
// must not become an implicit enable when the operator repairs permissions.
func TestFailedMarkerRemovalPreservesProtection(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged filesystem permission enforcement")
	}
	for _, marker := range []string{"email"} {
		t.Run(marker, func(t *testing.T) {
			dir := t.TempDir()
			control, err := instancecontrol.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			closeGate, openGate := control.DisableEmail, control.EnableEmail
			if err := closeGate(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(dir, 0700); err != nil {
					t.Error(err)
				}
			})
			if err := os.Chmod(dir, 0300); err != nil {
				t.Fatal(err)
			}
			if err := openGate(); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected permission failure, got %v", err)
			}
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			// Reconstruct the control as startup does, after storage repair.
			restarted, err := instancecontrol.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			// This is legacy import evidence, not modern mail authority.
			if disabled, err := restarted.LegacyEmailDisabled(); err != nil || !disabled {
				t.Fatalf("failed removal lost legacy protection: disabled=%v err=%v", disabled, err)
			}
			if err := openGate(); err != nil {
				t.Fatal(err)
			}
			if disabled, err := restarted.LegacyEmailDisabled(); err != nil || disabled {
				t.Fatalf("explicit legacy removal retry failed: disabled=%v err=%v", disabled, err)
			}
		})
	}
}
