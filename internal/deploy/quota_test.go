package deploy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

func TestDirSize_MissingPath(t *testing.T) {
	size, err := deploy.DirSize(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing path, got %v", err)
	}
	if size != 0 {
		t.Errorf("expected 0 bytes for missing path, got %d", size)
	}
}

func TestDirSize_EmptyDir(t *testing.T) {
	size, err := deploy.DirSize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Errorf("expected 0 bytes for empty dir, got %d", size)
	}
}

func TestDirSize_SumsRegularFilesRecursively(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), 10)
	writeFile(t, filepath.Join(root, "nested", "b.txt"), 25)
	writeFile(t, filepath.Join(root, "nested", "deep", "c.txt"), 7)

	size, err := deploy.DirSize(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(42); size != want {
		t.Errorf("expected %d bytes, got %d", want, size)
	}
}

func TestDirSize_IgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(t.TempDir(), "payload.txt")
	writeFile(t, payload, 100)

	link := filepath.Join(root, "payload.txt")
	if err := os.Symlink(payload, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	size, err := deploy.DirSize(root)
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Errorf("expected 0 bytes (symlink target should not be counted), got %d", size)
	}
}

func writeFile(t *testing.T, path string, nBytes int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, nBytes), 0644); err != nil {
		t.Fatal(err)
	}
}
