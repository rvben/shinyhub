package process

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAppCgroupDir pins the per-app cgroup naming so re-adoption reconstructs
// the exact directory Start created via placeInAppCgroup.
func TestAppCgroupDir(t *testing.T) {
	got := appCgroupDir("/sys/fs/cgroup/system.slice/shinyhub.service", "demo", 0)
	want := "/sys/fs/cgroup/system.slice/shinyhub.service/app-demo-0"
	if got != want {
		t.Fatalf("appCgroupDir = %q, want %q", got, want)
	}
}

// TestCgroupContainsPID verifies the membership check used to confirm an adopted
// PID really lives in the reconstructed cgroup before re-registering it.
func TestCgroupContainsPID(t *testing.T) {
	dir := t.TempDir()
	// Include a whitespace-padded line to exercise the TrimSpace hardening.
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("100\n4242\n  7777  \n200\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := cgroupContainsPID(dir, 4242)
	if err != nil || !ok {
		t.Fatalf("cgroupContainsPID(present) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, _ := cgroupContainsPID(dir, 7777); !ok {
		t.Fatalf("cgroupContainsPID must match a whitespace-padded PID line")
	}
	ok, err = cgroupContainsPID(dir, 999)
	if err != nil || ok {
		t.Fatalf("cgroupContainsPID(absent) = (%v, %v), want (false, nil)", ok, err)
	}
	// A PID whose decimal is a substring of a member must not match.
	ok, _ = cgroupContainsPID(dir, 42)
	if ok {
		t.Fatalf("cgroupContainsPID(42) matched a substring of 4242; want false")
	}
	// Missing cgroup.procs (cgroup gone) is an error so the caller degrades.
	if _, err := cgroupContainsPID(t.TempDir(), 1); err == nil {
		t.Fatalf("cgroupContainsPID on a missing cgroup.procs must error")
	}
}
