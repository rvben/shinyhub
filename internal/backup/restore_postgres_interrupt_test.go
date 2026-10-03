package backup_test

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestPostgresInterruptedRestoreRetainsOriginalDatabase(t *testing.T) {
	dbtest.RequirePostgres(t)
	if runtime.GOOS == "windows" {
		t.Skip("requires a Unix shell to synchronize the native restore tool")
	}
	requirePGTools(t)
	srcStore, srcDSN := dbtest.NewPostgres(t)
	requirePGDumpNewerThanServer(t, srcStore)
	if err := srcStore.CreateUser(db.CreateUserParams{Username: "replacement", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	// pg_restore cleans tables in reverse order. This table is dropped after
	// users, giving the test a deterministic place to interrupt a real restore.
	if _, err := srcStore.DB().Exec(`CREATE TABLE aa_restore_pause (value text)`); err != nil {
		t.Fatal(err)
	}
	src := mkPGCfg(t, srcDSN)
	seedFiles(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	dstStore, dstDSN := dbtest.NewPostgres(t)
	if err := dstStore.CreateUser(db.CreateUserParams{Username: "original", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dstStore.DB().Exec(`CREATE TABLE aa_restore_pause (value text)`); err != nil {
		t.Fatal(err)
	}
	dst := mkPGCfg(t, dstDSN)
	seedFiles(t, dst)
	tool, err := exec.LookPath("pg_restore")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	ready, release := filepath.Join(bin, "ready"), filepath.Join(bin, "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	// Delay only the external tool boundary until the rollback dump is done.
	// The actual restore, database writes and disconnection remain native.
	script := "#!/bin/sh\n: > " + quote(ready) + "\nwhile [ ! -e " + quote(release) + " ]; do sleep 0.01; done\nexec " + quote(tool) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := make(chan error, 1)
	go func() {
		_, err := backup.Restore(dst, archive)
		result <- err
	}()
	// Unblock the tool even if an assertion fails before the normal handoff.
	defer os.WriteFile(release, nil, 0o600)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not reach pg_restore")
		}
		time.Sleep(10 * time.Millisecond)
	}
	locker, err := sql.Open("pgx", dstDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	lock, err := locker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec(`LOCK TABLE aa_restore_pause IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline = time.Now().Add(30 * time.Second)
	for {
		err := dstStore.DB().QueryRow(`SELECT pid FROM pg_stat_activity
WHERE datname = current_database() AND pid <> pg_backend_pid()
AND wait_event_type = 'Lock' AND query LIKE '%aa_restore_pause%'`).Scan(&pid)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native restore did not reach the locked table")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var terminated bool
	if err := dstStore.DB().QueryRow(`SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("interrupt this fixture's restore: terminated=%v err=%v", terminated, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("interrupted restore reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("interrupted restore did not return")
	}
	if _, err := dstStore.GetUserByUsername("original"); err != nil {
		t.Fatalf("interruption lost the original database: %v", err)
	}
	if user, err := dstStore.GetUserByUsername("replacement"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("interruption imported replacement state: user=%+v err=%v", user, err)
	}
}
