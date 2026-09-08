package backup_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// closedPort returns a port number nothing is listening on, so a test can
// configure an address whose listener probe is guaranteed to find nothing.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// The guard's original two signals both reach the server through settings a
// maintenance invocation commonly omits: server.pid_file is opt-in and usually
// unset, and the listener probe is built from server.host/server.port, which
// fall back to 0.0.0.0:8080. Invoking `shinyhub restore` with only the
// database and storage paths - the same minimal environment `shinyhub backup`
// needs - therefore probed an address nothing was bound to, reported no
// running server, and renamed the live database out from under the server's
// open connection. The marker beside the database closes that gap, because the
// database path is the one setting restore cannot get wrong.
func TestRestoreRefusesWhenRuntimeMarkerNamesLiveServer(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	seed(t, dst)

	// The live server binds a port of its own choosing and publishes itself.
	live := *dst
	live.Server.Host = "127.0.0.1"
	live.Server.Port = 8118
	clear, err := backup.PublishRuntimeMarker(&live)
	if err != nil {
		t.Fatalf("PublishRuntimeMarker: %v", err)
	}
	defer clear()

	// The restoring invocation never mentioned that port, and no pid file is
	// configured, so the marker is the only signal that can fire.
	dst.Server.Host = "0.0.0.0"
	dst.Server.Port = closedPort(t)

	_, err = backup.Restore(dst, archive)
	if err == nil {
		t.Fatal("restore proceeded into a live server")
	}
	if !strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("want running-server refusal, got %v", err)
	}
	markerPath, ok := backup.RuntimeMarkerPath(dst)
	if !ok {
		t.Fatal("no marker path for a file-backed database")
	}
	// Naming the marker proves the refusal came from it and not from something
	// else that happened to be listening.
	if !strings.Contains(err.Error(), markerPath) {
		t.Errorf("refusal does not name the marker: %v", err)
	}
	if !strings.Contains(err.Error(), "8118") {
		t.Errorf("refusal does not report where the live server is serving: %v", err)
	}

	// Nothing may be touched before the refusal.
	matches, _ := filepath.Glob(dst.Database.DSN + ".pre-restore-*")
	if len(matches) != 0 {
		t.Errorf("guard ran destructive preserve() before refusing: found %v", matches)
	}
}

// A server that has stopped must not block the restore it was stopped for.
func TestRestoreProceedsAfterRuntimeMarkerIsCleared(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	dst.Server.Port = closedPort(t)
	clear, err := backup.PublishRuntimeMarker(dst)
	if err != nil {
		t.Fatalf("PublishRuntimeMarker: %v", err)
	}
	clear()

	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore after the server withdrew its marker: %v", err)
	}
}

// A marker left behind by a crash names a process that no longer exists.
// Treating it as live would wedge every future restore with no way out except
// deleting a file the operator has no reason to know about.
func TestRestoreIgnoresStaleRuntimeMarker(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	dst.Server.Port = closedPort(t)
	path, ok := backup.RuntimeMarkerPath(dst)
	if !ok {
		t.Fatal("no marker path for a file-backed database")
	}
	// A PID well outside any real process table.
	body, err := json.Marshal(backup.RuntimeMarker{PID: 1073741823, Addr: "127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore with a stale runtime marker: %v", err)
	}
}

// A zero-downtime upgrade runs two processes at once: the successor publishes
// under its own PID while the predecessor is still shutting down. If the
// predecessor's withdrawal removed the file unconditionally, the upgrade would
// end with a live server and no marker, silently reopening the gap this whole
// mechanism closes.
func TestRuntimeMarkerWithdrawalLeavesASuccessorsMarkerAlone(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seed(t, cfg)

	clearPredecessor, err := backup.PublishRuntimeMarker(cfg)
	if err != nil {
		t.Fatalf("PublishRuntimeMarker: %v", err)
	}
	path, _ := backup.RuntimeMarkerPath(cfg)

	// Stand in for the successor: same file, a different live PID.
	body, err := json.Marshal(backup.RuntimeMarker{PID: os.Getppid(), Addr: "127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	clearPredecessor()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("predecessor removed the successor's marker: %v", err)
	}
}

// Postgres has no local database file to sit beside, so there is no marker to
// publish and no marker path to read. Publishing must stay a no-op rather than
// an error that would abort startup.
func TestRuntimeMarkerIsAbsentForNonFileDatabases(t *testing.T) {
	cfg := mkCfg(t)
	cfg.Database.DSN = ":memory:"
	if _, ok := backup.RuntimeMarkerPath(cfg); ok {
		t.Error("an in-memory database reported a marker path")
	}
	clear, err := backup.PublishRuntimeMarker(cfg)
	if err != nil {
		t.Fatalf("PublishRuntimeMarker on a non-file database: %v", err)
	}
	clear()
}
