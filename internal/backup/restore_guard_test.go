package backup_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestRestoreRefusesWhenPIDFileNamesLiveProcess guards PROD-16: restoring
// while the server is up renames the live SQLite file (and its WAL) out from
// under an open connection. cfg.Server.PIDFile naming a still-running process
// is one of the two signals Restore checks before touching any state.
func TestRestoreRefusesWhenPIDFileNamesLiveProcess(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	seed(t, dst) // dst has its own live-looking state to protect
	pidFile := filepath.Join(t.TempDir(), "shinyhub.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o640); err != nil {
		t.Fatal(err)
	}
	dst.Server.PIDFile = pidFile

	if _, err := backup.Restore(dst, archive); err == nil ||
		!strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("want running-server refusal, got %v", err)
	}

	// The guard must short-circuit before any state is touched: no
	// .pre-restore-* sibling should exist next to the original DB file, and
	// the original file must still be at its original path.
	matches, _ := filepath.Glob(dst.Database.DSN + ".pre-restore-*")
	if len(matches) != 0 {
		t.Errorf("guard ran destructive preserve() before refusing: found %v", matches)
	}
	if _, statErr := os.Stat(dst.Database.DSN); statErr != nil {
		t.Errorf("guard must leave the existing db file at its original path: %v", statErr)
	}
}

// TestRestoreProceedsWhenPIDFileIsStale verifies a PID file left behind by an
// unclean crash (naming a process that no longer exists) does not block a
// legitimate restore.
func TestRestoreProceedsWhenPIDFileIsStale(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	pidFile := filepath.Join(t.TempDir(), "shinyhub.pid")
	// A PID well outside any real process table: stale, not live.
	if err := os.WriteFile(pidFile, []byte("1073741823"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst.Server.PIDFile = pidFile

	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore with a stale pid file: %v", err)
	}
}

// TestRestoreRefusesWhenListenerIsLive covers the common default (no
// server.pid_file configured): something already accepting connections on the
// app's configured host:port is the other running-server signal Restore
// checks.
func TestRestoreRefusesWhenListenerIsLive(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dst := mkCfg(t)
	dst.Server.Host = "127.0.0.1"
	dst.Server.Port = port

	if _, err := backup.Restore(dst, archive); err == nil ||
		!strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("want running-server refusal, got %v", err)
	}
}

// TestRestoreForceOverridesRunningServerGuard verifies RestoreForce lets an
// operator who has independently confirmed the server is stopped proceed past
// a matching signal (e.g. a PID file the operator knows is stale for their
// setup but that happens to still name a live PID belonging to something
// else).
func TestRestoreForceOverridesRunningServerGuard(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	pidFile := filepath.Join(t.TempDir(), "shinyhub.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o640); err != nil {
		t.Fatal(err)
	}
	dst.Server.PIDFile = pidFile

	if _, err := backup.RestoreForce(dst, archive, true); err != nil {
		t.Fatalf("RestoreForce(force=true): %v", err)
	}
}
