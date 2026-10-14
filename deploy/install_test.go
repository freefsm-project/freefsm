package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestFreeBSDInstallProtectsAndPreservesLiveConfig(t *testing.T) {
	root := t.TempDir()
	install := func() {
		t.Helper()
		cmd := exec.Command("sh", "freebsd/install.sh", root, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install: %v: %s", err, out)
		}
	}
	install()
	config := filepath.Join(root, "usr/local/etc/freefsm.conf")
	stat, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("live config mode %o; want 600", stat.Mode().Perm())
	}
	owner := stat.Sys().(*syscall.Stat_t)
	if int(owner.Uid) != os.Getuid() || int(owner.Gid) != os.Getgid() {
		t.Fatal("live config not owned by selected service identity")
	}
	// A second installation preserves secrets while tightening legacy modes.
	if err := os.WriteFile(config, []byte("retained-operator-config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0644); err != nil {
		t.Fatal(err)
	}
	install()
	value, err := os.ReadFile(config)
	if err != nil || string(value) != "retained-operator-config\n" {
		t.Fatal("operator configuration overwritten")
	}
	stat, err = os.Stat(config)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("legacy config permissions not restricted")
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/etc/rc.d/freefsm")); err != nil {
		t.Fatal("missing third-party rc.d service", err)
	}
	for _, path := range []string{"var/db/freefsm/state", "var/db/freefsm/uploads", "var/log/freefsm"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("make", "-C", "..", "install-freebsd", "DESTDIR="+root, "SERVICE_USER="+strconv.Itoa(os.Getuid()), "SERVICE_GROUP="+strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("staged Makefile install: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/bin/freefsm")); err != nil {
		t.Fatal(err)
	}
}

func TestFreeBSDInstallRejectsConfigSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "operator-file")
	if err := os.WriteFile(target, []byte("retained"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usr/local/etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "usr/local/etc/freefsm.conf")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "freebsd/install.sh", root, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("symlink accepted: %s", out)
	}
	stat, err := os.Stat(target)
	if err != nil || stat.Mode().Perm() != 0644 {
		t.Fatal("symlink target modified")
	}
}

func TestFreeBSDInstallPreservesAndRepairsExistingLog(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "var/log/freefsm/freefsm.log")
	if err := os.MkdirAll(filepath.Dir(log), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("retained service history\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "freebsd/install.sh", root, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v: %s", err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "retained service history\n" {
		t.Fatal("existing log lost", err)
	}
	stat, err := os.Stat(log)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("existing log permissions not repaired", err)
	}
}

func TestLinuxStagedInstallProvidesWritableLogAndRecoveryRoots(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("sh", "linux/install.sh", root, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Linux staging: %v: %s", err, out)
	}
	for _, path := range []string{"var/log/freefsm", "var/lib/freefsm", "var/lib/freefsm/uploads", "var/lib/freefsm/state"} {
		stat, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		owner := stat.Sys().(*syscall.Stat_t)
		if int(owner.Uid) != os.Getuid() || int(owner.Gid) != os.Getgid() || stat.Mode().Perm()&0200 == 0 {
			t.Fatalf("service cannot write %s", path)
		}
	}
	config := filepath.Join(root, "etc/freefsm.conf")
	stat, err := os.Stat(config)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("Linux EnvironmentFile not private", err)
	}
	if err := os.WriteFile(config, []byte("retained-config"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("make", "-C", "..", "install-linux", "DESTDIR="+root, "SERVICE_USER="+strconv.Itoa(os.Getuid()), "SERVICE_GROUP="+strconv.Itoa(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("staged Linux Makefile: %v: %s", err, out)
	}
	value, err := os.ReadFile(config)
	if err != nil || string(value) != "retained-config" {
		t.Fatal("Linux live config overwritten")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/freefsm.service")); err != nil {
		t.Fatal(err)
	}
}
