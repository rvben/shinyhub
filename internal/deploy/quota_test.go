package deploy_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/data"
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

func TestCheckAppQuota_Disabled(t *testing.T) {
	appsDir := t.TempDir()
	writeFile(t, filepath.Join(appsDir, "slug", "bundles", "a.zip"), int(2*deploy.MiB))

	used, err := deploy.CheckAppQuota(appsDir, "", "slug", 0)
	if err != nil {
		t.Fatalf("quotaMB=0 should disable the check, got error: %v", err)
	}
	if used != 2*deploy.MiB {
		t.Errorf("expected usage %d, got %d", 2*deploy.MiB, used)
	}
}

func TestCheckAppQuota_WithinLimit(t *testing.T) {
	appsDir := t.TempDir()
	writeFile(t, filepath.Join(appsDir, "slug", "bundles", "a.zip"), int(deploy.MiB))

	used, err := deploy.CheckAppQuota(appsDir, "", "slug", 2)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if used != deploy.MiB {
		t.Errorf("expected usage %d, got %d", deploy.MiB, used)
	}
}

func TestCheckAppQuota_Exceeded(t *testing.T) {
	appsDir := t.TempDir()
	writeFile(t, filepath.Join(appsDir, "slug", "bundles", "a.zip"), int(3*deploy.MiB))

	used, err := deploy.CheckAppQuota(appsDir, "", "slug", 2)
	if err == nil {
		t.Fatal("expected ErrQuotaExceeded, got nil")
	}
	if !errors.Is(err, deploy.ErrQuotaExceeded) {
		t.Errorf("expected error to wrap ErrQuotaExceeded, got %v", err)
	}
	if used != 3*deploy.MiB {
		t.Errorf("expected reported usage %d, got %d", 3*deploy.MiB, used)
	}
}

func TestCheckAppQuota_MissingSlugDirIsZero(t *testing.T) {
	appsDir := t.TempDir()
	used, err := deploy.CheckAppQuota(appsDir, "", "fresh-app", 2)
	if err != nil {
		t.Fatalf("missing app dir should return 0 bytes, got %v", err)
	}
	if used != 0 {
		t.Errorf("expected 0 bytes for fresh slug, got %d", used)
	}
}

func TestCheckAppQuota_IncludesDataDir(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDataDir := filepath.Join(root, "appdata")

	// Apps dir contribution: 6 bytes ("bundle").
	if err := os.MkdirAll(filepath.Join(appsDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "demo", "bundle.zip"), []byte("bundle"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Data dir contribution: 6 bytes ("dataaa") plus a 5-byte tempfile we
	// expect to be excluded.
	if err := os.MkdirAll(filepath.Join(appDataDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "demo", "x.parquet"), []byte("dataaa"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(appDataDir, "demo", data.UploadTempDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "demo", data.UploadTempDir, "scratch"), []byte("noooo"), 0o640); err != nil {
		t.Fatal(err)
	}

	used, err := deploy.CheckAppQuota(appsDir, appDataDir, "demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(12); used != want {
		t.Fatalf("used = %d, want %d (apps 6 + data 6, temp excluded)", used, want)
	}
}

func TestCheckAppQuota_EmptyAppDataDirSkipsDataContribution(t *testing.T) {
	appsDir := t.TempDir()
	writeFile(t, filepath.Join(appsDir, "slug", "bundle.zip"), int(deploy.MiB))

	used, err := deploy.CheckAppQuota(appsDir, "", "slug", 0)
	if err != nil {
		t.Fatal(err)
	}
	if used != deploy.MiB {
		t.Fatalf("used = %d, want %d", used, deploy.MiB)
	}
}

func TestCheckAppQuota_DataDirContributesToExceeded(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDataDir := filepath.Join(root, "appdata")

	// 1 MiB in apps dir, 2 MiB in data dir → total 3 MiB; quota 2 MiB → exceeded.
	writeFile(t, filepath.Join(appsDir, "slug", "bundle.zip"), int(deploy.MiB))
	writeFile(t, filepath.Join(appDataDir, "slug", "big.parquet"), int(2*deploy.MiB))

	used, err := deploy.CheckAppQuota(appsDir, appDataDir, "slug", 2)
	if err == nil {
		t.Fatal("expected ErrQuotaExceeded, got nil")
	}
	if !errors.Is(err, deploy.ErrQuotaExceeded) {
		t.Errorf("expected wrapped ErrQuotaExceeded, got %v", err)
	}
	if used != 3*deploy.MiB {
		t.Errorf("used = %d, want %d", used, 3*deploy.MiB)
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
