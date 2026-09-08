package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// A uv-managed .venv reaches its interpreter through symlinks, and one of them
// points outside the app tree into uv's shared Python store. Dropping those
// links produces a restored venv that has wrapper scripts but no interpreter -
// which uv will not repair, because it refuses to touch an existing .venv, so
// ShinyHub's own "prepared environment is missing; rebuilding it" self-heal
// fails too and the app is stuck. The operator sees a successful restore and
// only finds out at the next start.
func TestRoundTripPreservesVenvSymlinks(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)

	// uv's real layout: an absolute link out of the tree to the managed
	// interpreter, and a relative link beside it.
	store := filepath.Join(t.TempDir(), "uv-python", "bin", "python3.12")
	if err := os.MkdirAll(filepath.Dir(store), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(src.Storage.AppsDir, "demo", ".venv", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(binDir, "python3.12")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	if err := os.Symlink("python3.12", filepath.Join(binDir, "python")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dst := mkCfg(t)
	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	restoredBin := filepath.Join(dst.Storage.AppsDir, "demo", ".venv", "bin")
	for name, want := range map[string]string{"python3.12": store, "python": "python3.12"} {
		p := filepath.Join(restoredBin, name)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Errorf("%s is missing from the restored venv: %v", name, err)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s came back as %s, not a symlink", name, fi.Mode())
			continue
		}
		got, err := os.Readlink(p)
		if err != nil {
			t.Errorf("readlink %s: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", name, got, want)
		}
	}
}

// Recording symlinks verbatim means an archive can carry one that points
// anywhere. Extraction must therefore refuse to *follow* it: a later entry
// naming a path through that link must not write outside the restore tree.
func TestRestoreWillNotWriteThroughAnEscapingSymlink(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	outside := t.TempDir()
	appendEntries(t, archive,
		&tar.Header{
			Name:     "apps/escape",
			Linkname: outside,
			Typeflag: tar.TypeSymlink,
			Mode:     0o777,
		},
		&tar.Header{
			Name:     "apps/escape/pwned.txt",
			Typeflag: tar.TypeReg,
			Mode:     0o600,
			Size:     4,
		},
	)

	dst := mkCfg(t)
	_, err := backup.Restore(dst, archive)
	if err == nil {
		t.Error("restore accepted an archive that writes through an escaping symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "pwned.txt")); statErr == nil {
		t.Fatalf("restore wrote %s outside the restore tree", filepath.Join(outside, "pwned.txt"))
	}
}

// appendEntries rewrites archivePath with extra entries added after the
// existing ones. Bodies for regular headers are a run of 'x' of the declared
// size, which is all these tests need.
func appendEntries(t *testing.T, archivePath string, extra ...*tar.Header) {
	t.Helper()
	in, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gzr)

	rewritten := archivePath + ".rewritten"
	out, err := os.Create(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	gzw := gzip.NewWriter(out)
	tw := tar.NewWriter(gzw)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	for _, hdr := range extra {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			body := make([]byte, hdr.Size)
			for i := range body {
				body[i] = 'x'
			}
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []io.Closer{tw, gzw, out, gzr, in} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(rewritten, archivePath); err != nil {
		t.Fatal(err)
	}
}
