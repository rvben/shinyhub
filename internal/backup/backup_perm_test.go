package backup_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
)

// TestCreate_ArchiveIsOwnerOnly guards that the backup archive - which contains
// the full SQLite database including password and API-key hashes and the audit
// log - is created owner-read/write only, not world-readable.
func TestCreate_ArchiveIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	cfg := mkCfg(t)
	seed(t, cfg)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("backup archive mode = %o, want 0600 (it contains the full DB incl. password/key hashes and the audit log)", st.Mode().Perm())
	}
}
