package bundle

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

type zipEntry struct {
	name    string
	content string
	exec    bool
}

func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := os.FileMode(0o644)
		if e.exec {
			mode = 0o755
		}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatalf("create %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatalf("write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

func zipReader(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	return zr
}

func TestDigestStableAcrossEntryOrder(t *testing.T) {
	a := buildZip(t, []zipEntry{{"a.txt", "alpha", false}, {"b/c.txt", "gamma", false}})
	b := buildZip(t, []zipEntry{{"b/c.txt", "gamma", false}, {"a.txt", "alpha", false}})
	da, err := DigestZipReader(zipReader(t, a))
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := DigestZipReader(zipReader(t, b))
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da != db {
		t.Fatalf("digest must be entry-order independent: %s != %s", da, db)
	}
}

func TestDigestChangesOnContent(t *testing.T) {
	a := buildZip(t, []zipEntry{{"app.py", "print(1)", false}})
	b := buildZip(t, []zipEntry{{"app.py", "print(2)", false}})
	da, err := DigestZipReader(zipReader(t, a))
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := DigestZipReader(zipReader(t, b))
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da == db {
		t.Fatal("digest must change when file content changes")
	}
}

func TestDigestChangesOnExecBit(t *testing.T) {
	a := buildZip(t, []zipEntry{{"run.sh", "echo hi", false}})
	b := buildZip(t, []zipEntry{{"run.sh", "echo hi", true}})
	da, err := DigestZipReader(zipReader(t, a))
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := DigestZipReader(zipReader(t, b))
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da == db {
		t.Fatal("digest must change when the exec bit changes")
	}
}

func TestDigestIgnoresCacheDirEntries(t *testing.T) {
	with := buildZip(t, []zipEntry{{"app.py", "x", false}, {"__pycache__/app.pyc", "junk", false}})
	without := buildZip(t, []zipEntry{{"app.py", "x", false}})
	dw, err := DigestZipReader(zipReader(t, with))
	if err != nil {
		t.Fatalf("digest with: %v", err)
	}
	dwo, err := DigestZipReader(zipReader(t, without))
	if err != nil {
		t.Fatalf("digest without: %v", err)
	}
	if dw != dwo {
		t.Fatalf("cache-dir entries must not affect digest: %s != %s", dw, dwo)
	}
}

func TestDigestRejectsDataDirEntry(t *testing.T) {
	// "data/" is reserved by the platform (FilterRejectDataDir); the digest
	// must propagate this as a hard error rather than silently ignoring it.
	z := buildZip(t, []zipEntry{{"data/secret.csv", "x", false}})
	if _, err := DigestZipReader(zipReader(t, z)); err == nil {
		t.Fatal("digest must error on a rejected bundle entry")
	}
}

func TestDigestRejectsDuplicateName(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 2; i++ {
		w, _ := zw.Create("dup.txt")
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	if _, err := DigestZipReader(zipReader(t, buf.Bytes())); err == nil {
		t.Fatal("digest must error on duplicate accepted entry name")
	}
}

func TestDigestFormatIsStableContract(t *testing.T) {
	z := buildZip(t, []zipEntry{{"app.py", "print(1)", false}})
	d, err := DigestZipReader(zipReader(t, z))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.HasPrefix(d, "sha256:") {
		t.Fatalf("digest must be sha256:-prefixed, got %q", d)
	}
	// "sha256:" (7) + 64 hex chars of a 32-byte digest.
	if len(d) != 71 {
		t.Fatalf("digest length = %d, want 71 (sha256: + 64 hex)", len(d))
	}
	hexPart := strings.TrimPrefix(d, "sha256:")
	if _, err := hex.DecodeString(hexPart); err != nil {
		t.Fatalf("digest hex not decodable: %v", err)
	}
}

func TestDigestChangesOnFileName(t *testing.T) {
	a := buildZip(t, []zipEntry{{"app.py", "x", false}})
	b := buildZip(t, []zipEntry{{"main.py", "x", false}})
	da, err := DigestZipReader(zipReader(t, a))
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := DigestZipReader(zipReader(t, b))
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da == db {
		t.Fatal("digest must change when a file is renamed")
	}
}
