package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPut_PlantedSymlinkCannotEscape verifies a symlink planted inside the
// data dir (pointing at an outside directory) cannot redirect a Put write
// beyond the data dir. This is the realistic TOCTOU attack: the attacker
// pre-creates dataDir/escape -> /outside, then a later upload to
// "escape/pwned" must be rejected, not land in /outside.
func TestPut_PlantedSymlinkCannotEscape(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "appdata")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "escape")); err != nil {
		t.Fatal(err)
	}

	_, err := Put(dataDir, "escape/pwned", strings.NewReader("x"), 1)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Put through planted symlink err = %v, want ErrInvalidPath", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "pwned")); statErr == nil {
		t.Fatal("write escaped the data dir into the symlink target")
	}
}

// TestPut_TerminalSymlinkNotFollowed verifies that when the final path
// component is itself a symlink to an outside file, Put does not overwrite
// the target through it.
func TestPut_TerminalSymlinkNotFollowed(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "appdata")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataDir, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := Put(dataDir, "link", strings.NewReader("attacker"), 8); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Put onto terminal symlink err = %v, want ErrInvalidPath", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "original" {
		t.Fatalf("symlink target was overwritten: %q", b)
	}
}

// TestDelete_PlantedSymlinkCannotEscape verifies Delete cannot be tricked into
// unlinking a file outside the data dir via a planted directory symlink.
func TestDelete_PlantedSymlinkCannotEscape(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "appdata")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "escape")); err != nil {
		t.Fatal(err)
	}

	if err := Delete(dataDir, "escape/keep.txt"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Delete through planted symlink err = %v, want ErrInvalidPath", err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("victim file outside data dir was removed: %v", statErr)
	}
}

// TestPutDelete_HappyPathStillWorks guards against the symlink-safe rename
// path regressing ordinary uploads and deletes.
func TestPutDelete_HappyPathStillWorks(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "appdata")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}

	fi, err := Put(dataDir, "nested/dir/file.txt", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if fi.Size != 5 {
		t.Fatalf("size = %d, want 5", fi.Size)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "nested", "dir", "file.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("readback = %q err=%v", got, err)
	}

	if err := Delete(dataDir, "nested/dir/file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "nested", "dir", "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present after Delete: %v", err)
	}
}

// TestDelete_MissingDataDirIsNotFound covers an app whose per-app data dir was
// never created (no upload yet): deleting any path must report ErrFileNotFound,
// not a generic error. The Linux openat2 path opens the data dir itself and
// must defer to the portable fallback when that root is absent; without that,
// the API surfaces a 500 instead of a 404 (regression caught only on Linux,
// since darwin always takes the fallback).
func TestDelete_MissingDataDirIsNotFound(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "never-created")

	err := Delete(dataDir, "does-not-exist.txt")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("Delete on missing data dir = %v, want ErrFileNotFound", err)
	}
}
