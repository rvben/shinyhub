package backup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// requirePGTools skips when the libpq client binaries are unavailable. A
// Postgres-backed ShinyHub needs pg_dump/pg_restore on PATH to back up; the
// test exercises the same dependency the production code shells out to.
func requirePGTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH; skipping Postgres backup test", bin)
		}
	}
}

// requirePGDumpNewerThanServer skips when the local pg_dump predates the server.
// pg_dump refuses outright in that case ("aborting because of server version
// mismatch"), which is an environment mismatch rather than a defect: CI runs
// matching versions, while a developer machine commonly has an older Homebrew
// client than the server container. Reporting it as a failure is worse than
// useless - a suite that goes red for reasons unrelated to the code teaches
// people to stop reading it.
func requirePGDumpNewerThanServer(t *testing.T, store *db.Store) {
	t.Helper()
	var serverNum int
	if err := store.DB().QueryRow(`SHOW server_version_num`).Scan(&serverNum); err != nil {
		t.Fatalf("read server version: %v", err)
	}
	serverMajor := serverNum / 10000

	out, err := exec.Command("pg_dump", "--version").Output()
	if err != nil {
		t.Fatalf("pg_dump --version: %v", err)
	}
	// "pg_dump (PostgreSQL) 16.14 (Homebrew)" -> 16
	fields := strings.Fields(string(out))
	clientMajor := 0
	for _, f := range fields {
		if n, err := strconv.Atoi(strings.SplitN(f, ".", 2)[0]); err == nil && n > 8 {
			clientMajor = n
			break
		}
	}
	if clientMajor == 0 {
		t.Fatalf("could not parse pg_dump version from %q", strings.TrimSpace(string(out)))
	}
	if clientMajor < serverMajor {
		// In CI this is a provisioning error, not a developer's environment: the
		// workflow installs a client chosen to be >= the server. Skipping there
		// would silently drop this coverage the moment the distro default lags,
		// which is exactly the kind of loss a green suite should never hide.
		if os.Getenv("CI") != "" {
			t.Fatalf("pg_dump %d predates the server (%d); CI must install a client >= the server "+
				"or this round-trip stops being tested", clientMajor, serverMajor)
		}
		t.Skipf("pg_dump %d predates the server (%d); pg_dump refuses to dump a newer server. "+
			"Install client tools >= %d to run this test.", clientMajor, serverMajor, serverMajor)
	}
}

func mkPGCfg(t *testing.T, dsn string) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn},
		Storage: config.StorageConfig{
			AppsDir:    filepath.Join(root, "apps"),
			AppDataDir: filepath.Join(root, "app-data"),
		},
	}
}

func seedFiles(t *testing.T, cfg *config.Config) {
	t.Helper()
	for _, d := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		if err := os.MkdirAll(filepath.Join(d, "demo"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.Storage.AppsDir, "demo", "app.R"),
		[]byte("shinyApp(ui, server)"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Storage.AppDataDir, "demo", "state.csv"),
		[]byte("a,b\n1,2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresRoundTrip backs up a Postgres-backed instance via pg_dump and
// restores it into a fresh Postgres database via pg_restore, asserting the DB
// row and both file trees survive intact and the manifest records the backend.
func TestPostgresRoundTrip(t *testing.T) {
	dbtest.RequirePostgres(t)
	requirePGTools(t)

	srcStore, srcDSN := dbtest.NewPostgres(t)
	requirePGDumpNewerThanServer(t, srcStore)
	if _, err := srcStore.DB().Exec(
		`INSERT INTO users (username, password_hash, role) VALUES ('alice','x','admin')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	src := mkPGCfg(t, srcDSN)
	seedFiles(t, src)

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1.2.3", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, err := backup.ReadManifest(archive)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Backend != "postgres" {
		t.Errorf("manifest backend = %q, want postgres", m.Backend)
	}
	if m.ShinyHubVersion != "v1.2.3" {
		t.Errorf("manifest version = %q, want v1.2.3", m.ShinyHubVersion)
	}

	_, dstDSN := dbtest.NewPostgres(t)
	dst := mkPGCfg(t, dstDSN)
	moved, err := backup.Restore(dst, archive)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The pre-restore rollback dump holds full database contents, so it must be
	// owner-only like the archive itself, not left world-readable by the umask.
	var rollback string
	for _, p := range moved {
		if filepath.Ext(p) == ".dump" {
			rollback = p
		}
	}
	if rollback == "" {
		t.Fatalf("Restore did not produce a pre-restore .dump rollback; moved=%v", moved)
	}
	if fi, statErr := os.Stat(rollback); statErr != nil {
		t.Fatalf("stat rollback dump: %v", statErr)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("rollback dump mode = %o, want 600", perm)
	}

	dstStore, err := db.Open(dstDSN)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer dstStore.Close()
	var n int
	if err := dstStore.DB().QueryRow(
		`SELECT COUNT(*) FROM users WHERE username='alice'`).Scan(&n); err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if n != 1 {
		t.Errorf("restored user count = %d, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(dst.Storage.AppsDir, "demo", "app.R"))
	if err != nil || string(got) != "shinyApp(ui, server)" {
		t.Errorf("restored app.R = %q, err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dst.Storage.AppDataDir, "demo", "state.csv"))
	if err != nil || string(got) != "a,b\n1,2\n" {
		t.Errorf("restored state.csv = %q, err=%v", got, err)
	}
}
