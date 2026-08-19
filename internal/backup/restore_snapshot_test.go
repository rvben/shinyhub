package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
)

// writeSnapshot produces a pre-migration snapshot the way the startup path does:
// a bare SQLite file written by VACUUM INTO, with no tar or gzip wrapper.
func writeSnapshot(t *testing.T, dsn, dest string) {
	t.Helper()
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.BackupTo(dest); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
}

// An operator recovering from a schema downgrade reaches for the pre-migration
// snapshot the startup log just named, and hands it to `shinyhub restore`. That
// is the expected mistake, not an exotic one: the two artifacts are both called
// snapshots and only one is an archive. The error has to say what the file is
// and where to put it, at the moment the operator is under the most pressure.
func TestRestore_RejectsRawDatabaseFileWithGuidance(t *testing.T) {
	cfg := mkCfg(t)
	seed(t, cfg)
	snap := filepath.Join(t.TempDir(), "shinyhub.db.pre-migration-v58-20260819T091223Z.sqlite")
	writeSnapshot(t, cfg.Database.DSN, snap)

	// force=true: this test is about the archive check, not the running-server
	// guard, which has its own test.
	_, err := backup.RestoreForce(cfg, snap, true)
	if err == nil {
		t.Fatal("restoring a bare database file must fail, not silently proceed")
	}
	msg := err.Error()

	// Negative bound: the low-level decoder error must not be what reaches the
	// operator. Without this the test passes on the unfixed code, which does
	// return an error - just an unusable one.
	if strings.Contains(msg, "gzip") {
		t.Errorf("error leaks the gzip decoder failure instead of explaining the file:\n%s", msg)
	}
	// Positive bound: it names what the file is, the command that writes a real
	// archive, and the path the snapshot has to be moved to.
	for _, want := range []string{
		"not a shinyhub backup archive",
		"shinyhub backup",
		cfg.Database.DSN,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must mention %q, got:\n%s", want, msg)
		}
	}
}

// The database-file check must not swallow every archive failure: a file that is
// neither a database nor a valid archive still reports the archive problem.
// Without this bound, returning the snapshot guidance unconditionally would pass
// the test above.
func TestRestore_CorruptArchiveStillReportsArchiveFailure(t *testing.T) {
	cfg := mkCfg(t)
	seed(t, cfg)
	junk := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := os.WriteFile(junk, []byte("this is not an archive at all"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	_, err := backup.RestoreForce(cfg, junk, true)
	if err == nil {
		t.Fatal("restoring a junk file must fail")
	}
	if strings.Contains(err.Error(), "not a shinyhub backup archive") {
		t.Errorf("junk file must not be reported as a database snapshot:\n%s", err)
	}
}
