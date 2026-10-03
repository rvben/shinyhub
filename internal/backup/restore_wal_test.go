package backup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// Exit without closing SQLite to leave a real committed transaction in its
// WAL, as a stopped server can do after an unexpected process exit.
func TestRestorePreservesCommittedWALState(t *testing.T) {
	if path := os.Getenv("TEST_BACKUP_WAL_PATH"); path != "" {
		store, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		user, err := store.GetUserByUsername("alice")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateUserDisplayName(user.ID, "committed before crash"); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	dst := mkCfg(t)
	seed(t, dst)
	child := exec.Command(os.Args[0], "-test.run=^TestRestorePreservesCommittedWALState$")
	child.Env = append(os.Environ(), "TEST_BACKUP_WAL_PATH="+dst.Database.DSN)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("write committed WAL fixture: %v\n%s", err, output)
	}
	if info, err := os.Stat(dst.Database.DSN + "-wal"); err != nil || info.Size() <= 32 {
		t.Fatalf("fixture has no committed WAL frames: info=%v err=%v", info, err)
	}
	moved, err := backup.Restore(dst, archive)
	if err != nil {
		t.Fatal(err)
	}
	var preserved string
	for _, path := range moved {
		if strings.HasPrefix(path, dst.Database.DSN+".pre-restore-") && !strings.HasSuffix(path, "-wal") && !strings.HasSuffix(path, "-shm") {
			preserved = path
			break
		}
	}
	if preserved == "" {
		t.Fatalf("no preserved database: %v", moved)
	}
	previous, err := db.Open(preserved)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	user, err := previous.GetUserByUsername("alice")
	if err != nil || user.DisplayName != "committed before crash" {
		t.Fatalf("preserved database lost committed WAL state: user=%+v err=%v", user, err)
	}
	restored, err := db.Open(dst.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	user, err = restored.GetUserByUsername("alice")
	if err != nil || user.DisplayName != "" {
		t.Fatalf("old WAL contaminated restored snapshot: user=%+v err=%v", user, err)
	}
}
