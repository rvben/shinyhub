package data

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPut_Atomic(t *testing.T) {
	root := t.TempDir()
	dest := "out.bin"
	body := bytes.NewBufferString("hello world")
	info, err := Put(root, dest, body, int64(body.Len()))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info.Size != 11 {
		t.Errorf("size = %d", info.Size)
	}
	got, err := os.ReadFile(filepath.Join(root, dest))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q", got)
	}
	matches, _ := filepath.Glob(filepath.Join(root, UploadTempDir, "*"))
	if len(matches) != 0 {
		t.Errorf("tempfiles leftover: %v", matches)
	}
}

func TestPut_OverwritesExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString("new!")
	if _, err := Put(root, "x.txt", body, 4); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "x.txt"))
	if string(got) != "new!" {
		t.Errorf("content = %q", got)
	}
}

func TestPut_CreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	body := bytes.NewBufferString("nested")
	if _, err := Put(root, "a/b/c.txt", body, 6); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a", "b", "c.txt")); err != nil {
		t.Error(err)
	}
}

func TestPut_RejectsInvalidPath(t *testing.T) {
	root := t.TempDir()
	_, err := Put(root, "../escape", strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPut_RejectsSymlinkedParentDir(t *testing.T) {
	// An app planting a directory symlink inside its data dir must not be
	// able to escape the sandbox via subsequent writes. The classic vector:
	//   data/<slug>/sub -> /etc
	//   PUT /api/apps/<slug>/data/sub/passwd  → would write /etc/passwd
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	_, err := Put(root, "sub/escape.txt", strings.NewReader("pwned"), 5)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath for write through symlinked parent, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(statErr) {
		t.Errorf("file landed outside the data dir at %s — symlink traversal was NOT prevented", filepath.Join(outside, "escape.txt"))
	}
}

func TestPut_RejectsSymlinkedDestination(t *testing.T) {
	// A symlink at the destination path itself (not just a parent) must be
	// rejected too — even if os.Rename happens to replace the link rather
	// than follow it, downstream tooling reading the file would follow it.
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("preexisting"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "secret")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	_, err := Put(root, "secret", strings.NewReader("overwrite"), 9)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath when destination path is a symlink, got %v", err)
	}
}

func TestDelete_RejectsSymlinkedParentDir(t *testing.T) {
	// Same guard as Put: an attacker who plants a directory symlink must not
	// be able to unlink files outside the data dir via Delete.
	root := t.TempDir()
	outside := t.TempDir()
	victimPath := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victimPath, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	err := Delete(root, "sub/victim.txt")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath for delete through symlinked parent, got %v", err)
	}
	if _, statErr := os.Stat(victimPath); statErr != nil {
		t.Errorf("victim file %s was deleted via symlink traversal: %v", victimPath, statErr)
	}
}

func TestDelete_RejectsSymlinkAtFinalSegment(t *testing.T) {
	// Removing a symlink itself is fine in principle, but Delete()'s contract
	// is "remove a regular file". A symlink at the final segment must be
	// rejected so callers don't get surprised by silent unlinks of pointers
	// to outside files (and to keep the rule symmetric with Put).
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "real")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	err := Delete(root, "link")
	if !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrNotAFile) {
		t.Fatalf("expected ErrInvalidPath or ErrNotAFile when deleting a symlink, got %v", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("target file %s was removed via symlink delete: %v", target, statErr)
	}
}

func TestPut_ConcurrentLastWriterWins(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			_, _ = Put(root, "race.txt",
				strings.NewReader(strings.Repeat(string('A'+rune(i)), 10)), 10)
		}()
	}
	wg.Wait()
	got, err := os.ReadFile(filepath.Join(root, "race.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("expected 10-byte file (atomic rename), got %d", len(got))
	}
	matches, _ := filepath.Glob(filepath.Join(root, UploadTempDir, "*"))
	if len(matches) != 0 {
		t.Errorf("tempfiles leftover: %v", matches)
	}
}

func TestPut_ComputesSHA256(t *testing.T) {
	root := t.TempDir()
	info, err := Put(root, "x", io.NopCloser(strings.NewReader("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %s", info.SHA256)
	}
}

func TestList_SortedAndExcludesTempDir(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"b.txt", "a.txt", "sub/c.txt"} {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o750)
		_ = os.WriteFile(full, []byte("x"), 0o640)
	}
	// Write a file inside the temp dir — must NOT appear in results.
	tmpDir := filepath.Join(root, UploadTempDir)
	_ = os.MkdirAll(tmpDir, 0o750)
	_ = os.WriteFile(filepath.Join(tmpDir, "scratch"), []byte("x"), 0o640)

	files, err := List(root, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d: %v", len(files), files)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	want := []string{"a.txt", "b.txt", "sub/c.txt"}
	for i, w := range want {
		if paths[i] != w {
			t.Errorf("files[%d].Path = %q, want %q", i, paths[i], w)
		}
	}
}

func TestList_CapEnforced(t *testing.T) {
	root := t.TempDir()
	for i := range 5 {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.txt", i)), []byte("x"), 0o640)
	}
	_, err := List(root, 3)
	if !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("want ErrTooManyFiles, got %v", err)
	}
}

func TestList_MissingDir(t *testing.T) {
	files, err := List("/nonexistent/path/xyz", 100)
	if err != nil {
		t.Fatalf("missing dir should be treated as empty, got: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want 0 files, got %d", len(files))
	}
}

func TestDelete_RemovesFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "hello.txt")
	_ = os.WriteFile(p, []byte("hi"), 0o640)
	if err := Delete(root, "hello.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("file should be gone")
	}
}

func TestDelete_RefusesDirectory(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "subdir"), 0o750)
	err := Delete(root, "subdir")
	if !errors.Is(err, ErrNotAFile) {
		t.Fatalf("want ErrNotAFile, got %v", err)
	}
}

func TestDelete_RefusesReservedPrefix(t *testing.T) {
	root := t.TempDir()
	if err := Delete(root, ".shinyhub-foo"); err == nil {
		t.Fatal("want error for reserved prefix")
	}
}

func TestDelete_NotFound(t *testing.T) {
	root := t.TempDir()
	err := Delete(root, "missing.txt")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("want ErrFileNotFound, got %v", err)
	}
}

func TestDirSize_ExcludesTempDir(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a"), []byte("abcd"), 0o640) // 4 bytes
	tmpDir := filepath.Join(root, UploadTempDir)
	_ = os.MkdirAll(tmpDir, 0o750)
	_ = os.WriteFile(filepath.Join(tmpDir, "scratch"), []byte("abcd"), 0o640)

	size, err := DirSize(root)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 4 {
		t.Errorf("want 4, got %d", size)
	}
}

func TestCleanupUploadTemp_RemovesOldEntries(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, UploadTempDir)
	_ = os.MkdirAll(tmpDir, 0o750)

	oldFile := filepath.Join(tmpDir, "old")
	freshFile := filepath.Join(tmpDir, "fresh")
	_ = os.WriteFile(oldFile, []byte("x"), 0o640)
	_ = os.WriteFile(freshFile, []byte("x"), 0o640)

	// Backdate the old file by 2 hours.
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(oldFile, past, past)

	if err := CleanupUploadTemp(root, time.Hour); err != nil {
		t.Fatalf("CleanupUploadTemp: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should be removed")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Errorf("fresh file should remain: %v", err)
	}
}
