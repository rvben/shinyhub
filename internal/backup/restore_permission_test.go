package backup_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestRestoreRetryAfterPermissionFailureRetainsRollback(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions enforced for a non-root user")
	}
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	dst := mkCfg(t)
	parent := filepath.Join(t.TempDir(), "protected")
	dst.Storage.AppsDir = filepath.Join(parent, "apps")
	seed(t, dst)
	store, err := db.Open(dst.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("alice")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.UpdateUserDisplayName(user.ID, "before restore"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	oldFiles := map[string]string{
		filepath.Join(dst.Storage.AppsDir, "demo", "app.R"):        "old app",
		filepath.Join(dst.Storage.AppDataDir, "demo", "state.csv"): "old data",
	}
	for path, body := range oldFiles {
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	moved, err := backup.Restore(dst, archive)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected failure preserving the app tree: moved=%v err=%v", moved, err)
	}
	var rollback string
	for _, path := range moved {
		if strings.HasPrefix(path, dst.Database.DSN+".pre-restore-") && !strings.HasSuffix(path, "-wal") && !strings.HasSuffix(path, "-shm") {
			rollback = path
			break
		}
	}
	if rollback == "" {
		t.Fatal("failure omitted the already preserved database")
	}
	for path, want := range oldFiles {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("permission failure changed existing files: path=%s got=%q err=%v", path, got, err)
		}
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("retry after repairing permissions: %v", err)
	}
	for path, wantName := range map[string]string{rollback: "before restore", dst.Database.DSN: ""} {
		store, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		user, err := store.GetUserByUsername("alice")
		store.Close()
		if err != nil || user.DisplayName != wantName {
			t.Fatalf("retry did not preserve the correct database: path=%s user=%+v err=%v", path, user, err)
		}
	}
	for path, want := range map[string]string{
		filepath.Join(dst.Storage.AppsDir, "demo", "app.R"):        "shinyApp(ui, server)",
		filepath.Join(dst.Storage.AppDataDir, "demo", "state.csv"): "a,b\n1,2\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("retry did not restore archive files: path=%s got=%q err=%v", path, got, err)
		}
	}
}
