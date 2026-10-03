package backup_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestRepeatedRestoreRetainsEveryPreviousDatabase(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	dst := mkCfg(t)
	seed(t, dst)
	previous := make(map[string]string)
	for i := range 4 {
		store, err := db.Open(dst.Database.DSN)
		if err != nil {
			t.Fatal(err)
		}
		user, err := store.GetUserByUsername("alice")
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		name := fmt.Sprintf("state before restore %d", i)
		if err := store.UpdateUserDisplayName(user.ID, name); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		moved, restoreErr := backup.Restore(dst, archive)
		// Even a failed retry must not replace an earlier rollback database.
		for path, want := range previous {
			old, err := db.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			user, err := old.GetUserByUsername("alice")
			old.Close()
			if err != nil || user.DisplayName != want {
				t.Fatalf("prior rollback overwritten: path=%s user=%+v want=%q err=%v; restore error=%v", path, user, want, err, restoreErr)
			}
		}
		if restoreErr != nil {
			t.Fatalf("repeat restore failed: %v", restoreErr)
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
		previous[preserved] = name
	}
	if len(previous) != 4 {
		t.Fatalf("only %d distinct rollback databases survived", len(previous))
	}
}
