package deploy_test

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

type hardeningZipEntry struct {
	name    string
	mode    os.FileMode
	body    string
	symlink bool
}

func buildHardeningZip(t *testing.T, path string, entries []hardeningZipEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		m := e.mode
		if e.symlink {
			m |= os.ModeSymlink
		}
		fh.SetMode(m)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractBundle_CapsFileMode(t *testing.T) {
	// umask(0) so the extracted mode reflects the cap, not the process umask.
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "b.zip")
	buildHardeningZip(t, zipPath, []hardeningZipEntry{
		{name: "app.py", mode: 0o666, body: "x"}, // world-writable -> capped, non-exec
		{name: "run.py", mode: 0o777, body: "y"}, // world-writable + exec -> capped, exec kept
	})
	dest := filepath.Join(dir, "out")
	if err := deploy.ExtractBundle(zipPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	app, err := os.Stat(filepath.Join(dest, "app.py"))
	if err != nil {
		t.Fatal(err)
	}
	if app.Mode().Perm() != 0o644 {
		t.Errorf("app.py mode = %o, want 0644 (no group/world write)", app.Mode().Perm())
	}
	run, err := os.Stat(filepath.Join(dest, "run.py"))
	if err != nil {
		t.Fatal(err)
	}
	if run.Mode().Perm() != 0o755 {
		t.Errorf("run.py mode = %o, want 0755 (exec kept, no group/world write)", run.Mode().Perm())
	}
}

func TestExtractBundle_RejectsSymlinkEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "b.zip")
	buildHardeningZip(t, zipPath, []hardeningZipEntry{
		{name: "app.py", mode: 0o644, body: "x"},
		{name: "link.py", symlink: true, mode: 0o777, body: "/etc/passwd"},
	})
	err := deploy.ExtractBundle(zipPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error for symlink entry, got nil")
	}
}

func TestExtractBundle_RejectsTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "b.zip")
	entries := make([]hardeningZipEntry, 0, 10001)
	for i := 0; i < 10001; i++ {
		entries = append(entries, hardeningZipEntry{name: fmt.Sprintf("f%d.py", i), mode: 0o644})
	}
	buildHardeningZip(t, zipPath, entries)

	err := deploy.ExtractBundle(zipPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error for excessive entry count, got nil")
	}
	if !errors.Is(err, deploy.ErrBundleTooLarge) {
		t.Errorf("want ErrBundleTooLarge, got %v", err)
	}
}
