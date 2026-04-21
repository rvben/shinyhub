package api

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBundleUpload_TooLarge(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/apps/app/deploy", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	file, cleanup, err := readBundleUpload(rec, req, 8)
	defer cleanup()
	if file != nil {
		file.Close()
		t.Fatal("expected no bundle file for oversized upload")
	}
	if err != errBundleTooLarge {
		t.Fatalf("expected errBundleTooLarge, got %v", err)
	}
}

// TestReadBundleUpload_RemovesSpillFiles guards against the regression where
// large multipart uploads spilled to disk in os.TempDir and the cleanup
// returned by readBundleUpload was missing. Each upload would then leak a
// "multipart-*" temp file until the OS reaper ran.
func TestReadBundleUpload_RemovesSpillFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	// Shrink the in-memory threshold so a small payload still spills to disk.
	prev := maxBundleMemoryBuffer
	maxBundleMemoryBuffer = 16
	t.Cleanup(func() { maxBundleMemoryBuffer = prev })

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	// Write enough payload to force a disk spill (well above 16 bytes).
	payload := bytes.Repeat([]byte("x"), 4096)
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/apps/app/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	file, cleanup, err := readBundleUpload(rec, req, 1<<20)
	if err != nil {
		t.Fatalf("readBundleUpload: %v", err)
	}

	if got := countSpillFiles(t, tmpDir); got == 0 {
		t.Fatalf("expected multipart spill files in %s after parse, got none", tmpDir)
	}

	cleanup()

	if got := countSpillFiles(t, tmpDir); got != 0 {
		t.Fatalf("expected zero spill files after cleanup, got %d", got)
	}
	// Reading from the file after cleanup must fail; the underlying tempfile is gone.
	if _, err := file.Read(make([]byte, 1)); err == nil {
		t.Errorf("read after cleanup should fail; the temp file was removed")
	}
}

func countSpillFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "multipart-") {
			count++
			continue
		}
		// Some platforms nest spill files under sub-dirs; recurse one level.
		if e.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(dir, e.Name()))
			for _, se := range subEntries {
				if strings.HasPrefix(se.Name(), "multipart-") {
					count++
				}
			}
		}
	}
	return count
}
