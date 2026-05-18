package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

func TestDigestLocalDir_MatchesServerDigestOfSameZip(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print('hi')\n")
	mustWrite(t, filepath.Join(dir, "sub", "data.txt"), "payload\n")

	local, err := digestLocalDir(dir)
	if err != nil {
		t.Fatalf("digestLocalDir: %v", err)
	}
	if local == "" || local[:7] != "sha256:" {
		t.Fatalf("digest = %q, want sha256: prefix", local)
	}

	// Server parity: zip the same tree the way the CLI uploads, then run the
	// exact server-side digest function over those bytes.
	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	server, err := bundle.DigestZipReader(zr)
	if err != nil {
		t.Fatalf("DigestZipReader: %v", err)
	}
	if local != server {
		t.Fatalf("client/server digest parity broken:\n client=%s\n server=%s", local, server)
	}
}

func TestDigestLocalDir_StableAndContentSensitive(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "one\n")
	d1, err := digestLocalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := digestLocalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest not stable: %s != %s", d1, d2)
	}
	mustWrite(t, filepath.Join(dir, "a.txt"), "two\n")
	d3, err := digestLocalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("digest did not change after a content edit")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
