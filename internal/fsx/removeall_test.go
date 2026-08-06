package fsx_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rvben/shinyhub/internal/fsx"
)

// renvSandboxTree builds the shape renv's package sandbox leaves behind: a
// directory with a file in it, chmod'ed to 0555 so the file inside cannot be
// unlinked. This is the exact tree that made an R app permanently
// undeletable in production ("unlinkat .../.renv-sandbox: permission denied").
func renvSandboxTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sandboxDir := filepath.Join(root, "versions", "1", ".cache", "R", "renv",
		"sandbox", "linux-ubuntu-noble", "R-4.6", "x86_64-pc-linux-gnu", "9a444a72")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandboxDir, ".renv-sandbox"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sandboxDir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore write access unconditionally so a failing test still cleans up:
	// t.TempDir's own removal hits the same wall os.RemoveAll does.
	t.Cleanup(func() { _ = os.Chmod(sandboxDir, 0o755) })
	return root
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks, so there is nothing to defeat")
	}
}

// TestRemoveAll_DefeatsReadOnlyDir is the regression for the production
// defect. os.RemoveAll fails on this tree; the assertion below both proves
// that (so the test cannot pass vacuously) and that fsx.RemoveAll succeeds.
func TestRemoveAll_DefeatsReadOnlyDir(t *testing.T) {
	skipIfRoot(t)
	root := renvSandboxTree(t)

	// Negative control: the standard library cannot do this. If this ever
	// stops failing, the test below no longer proves anything.
	if err := os.RemoveAll(root); err == nil {
		t.Fatal("os.RemoveAll unexpectedly removed a 0555 directory's contents; " +
			"the premise of fsx.RemoveAll no longer holds on this platform")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("os.RemoveAll: want a permission error, got %v", err)
	}

	if err := fsx.RemoveAll(root); err != nil {
		t.Fatalf("fsx.RemoveAll: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("root still present after RemoveAll: stat err = %v", err)
	}
}

// TestRemoveAll_UnreadableDir covers the mode that also blocks the repair walk
// itself: a directory with no owner read bit cannot be listed, so widening has
// to happen before the walk descends rather than after.
func TestRemoveAll_UnreadableDir(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	inner := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(inner, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "child", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inner, 0o111); err != nil { // --x--x--x: traversable, not listable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o755) })

	if err := fsx.RemoveAll(root); err != nil {
		t.Fatalf("fsx.RemoveAll: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("root still present: stat err = %v", err)
	}
}

// TestRemoveAll_DoesNotFollowSymlinks pins that the repair walk never chmods
// or descends through a symlink: widening a link target would change
// permissions on bytes outside the tree being deleted.
func TestRemoveAll_DoesNotFollowSymlinks(t *testing.T) {
	skipIfRoot(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.MkdirAll(victim, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(victim, 0o755) })

	root := t.TempDir()
	// A read-only dir forces the repair path to run, and the symlink beside it
	// is what the walk must decline to follow.
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if err := fsx.RemoveAll(root); err != nil {
		t.Fatalf("fsx.RemoveAll: %v", err)
	}
	fi, err := os.Lstat(victim)
	if err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o555 {
		t.Fatalf("symlink target mode changed: got %o, want 555 (the walk followed the link)", got)
	}
}

// TestRemoveAll_MissingPathIsNil keeps parity with os.RemoveAll for the
// already-gone case, which the delete and prune paths both rely on.
func TestRemoveAll_MissingPathIsNil(t *testing.T) {
	if err := fsx.RemoveAll(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("want nil for a missing path, got %v", err)
	}
}

// TestRemoveAll_ReportsUnfixableParent pins that a failure this package cannot
// legitimately repair is reported rather than swallowed: when the PARENT
// denies write, removing the child is not ours to force.
func TestRemoveAll_ReportsUnfixableParent(t *testing.T) {
	skipIfRoot(t)
	parent := filepath.Join(t.TempDir(), "parent")
	target := filepath.Join(parent, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	if err := fsx.RemoveAll(target); err == nil {
		t.Fatal("want an error when the parent denies write, got nil")
	}
}
