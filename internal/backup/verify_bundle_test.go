package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// seedAppWithActiveDeployment adds one app to cfg's DB with an active
// deployment whose bundle_dir is a real, populated version directory under
// cfg.Storage.AppsDir, mirroring what a real deploy commits and lays out on
// disk (the shape Verify's active-bundle cross-check must recognize).
func seedAppWithActiveDeployment(t *testing.T, cfg *config.Config, slug, version string) {
	t.Helper()
	dbtest.WriteSQLiteFile(t, cfg.Database.DSN)
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	if _, err := store.DB().Exec(
		`INSERT INTO users (username, password_hash, role) VALUES ('bob','x','admin')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := store.DB().QueryRow(`SELECT id FROM users WHERE username='bob'`).Scan(&userID); err != nil {
		t.Fatalf("read seeded user id: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, ProjectSlug: slug, OwnerID: userID, Access: "private",
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	versionDir := filepath.Join(cfg.Storage.AppsDir, slug, "versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, "app.R"), []byte("shinyApp(ui, server)"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: version, BundleDir: versionDir,
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
}

// TestVerify_CleanArchiveWithActiveDeployment is the no-false-positive
// baseline for the active-bundle cross-check below: an app with an active
// deployment whose version directory really is archived must verify clean.
func TestVerify_CleanArchiveWithActiveDeployment(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seedAppWithActiveDeployment(t, cfg, "demo", "20260101T000000Z")

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backup.Verify(archive); err != nil {
		t.Errorf("Verify on a consistent archive: %v", err)
	}
}

// TestVerify_DetectsActiveBundleMissingFromArchive regresses the core failure
// mode of the Create race: a DB snapshot naming an app's active deployment
// whose version directory did not make it into the archived filesystem tree.
// That is exactly the shape a deploy+prune racing the old, unfenced
// snapshot-to-walk gap in Create produced (see
// TestCreate_FencesConcurrentDeployDuringSnapshotWalk in the internal
// package), reproduced here directly against Verify by surgically dropping
// the version directory's tar entries from an otherwise good archive, so this
// exercises Verify's own detection independent of Create's fence.
func TestVerify_DetectsActiveBundleMissingFromArchive(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seedAppWithActiveDeployment(t, cfg, "demo", "20260101T000000Z")

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	corrupted := filepath.Join(t.TempDir(), "corrupted.tar.gz")
	dropTarPrefix(t, archive, corrupted, "apps/demo/versions/20260101T000000Z")

	err := backup.Verify(corrupted)
	if err == nil {
		t.Fatal("want error verifying an archive whose active deployment's bundle dir is missing, got nil")
	}
	if !strings.Contains(err.Error(), "demo") {
		t.Errorf("error should name the affected app slug, got: %v", err)
	}
}

// dropTarPrefix copies every entry of the gzipped tar at src to dst except
// those named exactly prefix or nested under it, reproducing what a prune
// racing the backup's filesystem walk would have left out.
func dropTarPrefix(t *testing.T, src, dst, prefix string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	gzr, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
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
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			if _, err := io.Copy(io.Discard, tr); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
