// internal/worker/agent/persist_test.go
package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPersistAtomically_WritesContentAndLeavesNoTempFile verifies the plain
// success path: the target file carries the new content and no stray
// ".tmp-*" file is left behind in the directory.
func TestPersistAtomically_WritesContentAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.pem")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := persistAtomically(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("persistAtomically: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("target content = %q, want %q", got, "new")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind after a successful persist: %s", e.Name())
		}
	}
}

// TestPersistAtomically_SucceedsDespiteReadOnlyTargetFile proves persistAtomically
// commits via rename rather than an in-place truncate-and-write: a rename only
// needs write permission on the containing directory, not on the file it
// replaces, so persistAtomically must succeed even when the existing target
// file's own permission bits are read-only. A naive os.WriteFile-based
// implementation fails this same setup (verified directly against
// os.WriteFile below), so this is the property that distinguishes an atomic
// write from an in-place one.
func TestPersistAtomically_SucceedsDespiteReadOnlyTargetFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.pem")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	// Confirm the premise: a plain in-place write is what this setup defeats.
	if err := os.WriteFile(path, []byte("naive"), 0o600); err == nil {
		t.Fatal("premise broken: os.WriteFile succeeded against a read-only target file")
	}

	if _, err := persistAtomically(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("persistAtomically: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("target content = %q, want %q", got, "new")
	}
}

// TestPersistAtomically_CreateTempFailureLeavesTargetUntouched verifies that a
// failure before the rename (the commit point) leaves the target file exactly
// as it was and returns a non-nil error.
func TestPersistAtomically_CreateTempFailureLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.pem")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	prev := createTempHook
	createTempHook = func(string, string) (*os.File, error) {
		return nil, errors.New("simulated disk full")
	}
	t.Cleanup(func() { createTempHook = prev })

	if _, err := persistAtomically(path, []byte("new"), 0o600); err == nil {
		t.Fatal("persistAtomically succeeded despite a failing createTempHook")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, []byte("old")) {
		t.Errorf("target content changed despite the failed persist: got %q, want %q", got, "old")
	}
}

// TestPersistAtomically_DirSyncFailureReturnsWarningNotError verifies that once
// the rename has committed, a failure of the trailing parent-directory fsync
// is reported only as dirErr, not err: the write already succeeded under its
// final name, so the caller should treat this as a durability warning, not a
// failed persist.
func TestPersistAtomically_DirSyncFailureReturnsWarningNotError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.pem")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	prev := syncDirHook
	syncDirHook = func(string) error { return errors.New("simulated dir fsync failure") }
	t.Cleanup(func() { syncDirHook = prev })

	dirErr, err := persistAtomically(path, []byte("new"), 0o600)
	if err != nil {
		t.Fatalf("persistAtomically returned a hard error for a dir-sync-only failure: %v", err)
	}
	if dirErr == nil {
		t.Error("expected a non-nil dirErr for the failed directory fsync")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("target content = %q, want %q (rename should have already committed)", got, "new")
	}
}
