package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestVerify_CleanArchive checks that a healthy archive produced by Create
// verifies without error - the non-corrupt baseline for the detection tests
// below.
func TestVerify_CleanArchive(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seed(t, cfg)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backup.Verify(archive); err != nil {
		t.Errorf("Verify on a healthy archive: %v", err)
	}
}

// TestVerify_DetectsTruncatedArchive guards PROD-17: a short write on a flaky
// filesystem must be caught as an error, not silently reported as a good
// backup. It truncates a real Create-produced archive (not a hand-rolled
// fake) to reproduce that failure mode against the real tar/gzip format.
func TestVerify_DetectsTruncatedArchive(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seed(t, cfg)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fi, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(archive, fi.Size()/2); err != nil {
		t.Fatal(err)
	}
	if err := backup.Verify(archive); err == nil {
		t.Fatal("want error verifying a truncated archive, got nil")
	}
}

// TestVerify_DetectsMissingDBEntry checks the "structurally valid tar/gzip but
// missing the DB snapshot" case (e.g. a write interrupted right after the
// manifest closed the tar trailer early): Verify must report it even though
// the file opens and decodes cleanly. Reuses the manifest-only archive helper
// from backup_test.go so the fixture matches the real on-disk manifest format
// exactly.
func TestVerify_DetectsMissingDBEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "manifest-only.tar.gz")
	rewriteManifestSchema(t, archive, 1)
	err := backup.Verify(archive)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want missing-db-entry error, got %v", err)
	}
}
