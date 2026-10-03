package backup_test

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestPostgresFailedRestoreLeavesPreviousUsersIntact(t *testing.T) {
	dbtest.RequirePostgres(t)
	requirePGTools(t)
	srcStore, srcDSN := dbtest.NewPostgres(t)
	requirePGDumpNewerThanServer(t, srcStore)
	if err := srcStore.CreateUser(db.CreateUserParams{Username: "replacement", PasswordHash: "x", Role: "admin"}); err != nil {
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
	// A local view not present in the backup prevents dropping users. This
	// produces a real SQL restore error, without replacing pg_restore itself.
	if _, err := dstStore.DB().Exec(`CREATE VIEW restore_failure_probe AS SELECT username FROM users`); err != nil {
		t.Fatal(err)
	}
	dst := mkPGCfg(t, dstDSN)
	seedFiles(t, dst)
	moved, err := backup.Restore(dst, archive)
	if err == nil || !strings.Contains(err.Error(), "pg_restore") {
		t.Fatalf("expected a native PostgreSQL restore error: moved=%v err=%v", moved, err)
	}
	if _, err := dstStore.GetUserByUsername("original"); err != nil {
		t.Errorf("failed restore removed the original user: %v", err)
	}
	if user, err := dstStore.GetUserByUsername("replacement"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("failed restore changed replacement-user lookup: user=%+v err=%v", user, err)
	}
	// Prove that the separately saved rollback is usable, rather than merely
	// asserting that a nonempty .dump file exists.
	var rollback string
	for _, path := range moved {
		if filepath.Ext(path) == ".dump" {
			rollback = path
			break
		}
	}
	if rollback == "" {
		t.Fatal("failed restore did not return a rollback dump")
	}
	if _, err := dstStore.DB().Exec(`DROP VIEW restore_failure_probe`); err != nil {
		t.Fatal(err)
	}
	conn, err := url.Parse(dstDSN)
	if err != nil {
		t.Fatal("invalid fixture connection URI")
	}
	env := os.Environ()
	if conn.User != nil {
		if password, ok := conn.User.Password(); ok {
			env = append(env, "PGPASSWORD="+password)
			conn.User = url.User(conn.User.Username())
		}
	}
	cmd := exec.Command("pg_restore", "--single-transaction", "--clean", "--if-exists", "--no-owner", "--no-privileges", "--dbname", conn.String(), rollback)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("recover saved rollback: %v\n%s", err, out)
	}
	if _, err := dstStore.GetUserByUsername("original"); err != nil {
		t.Fatalf("rollback did not recover the original user: %v", err)
	}
	if user, err := dstStore.GetUserByUsername("replacement"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("rollback did not recover replacement-user lookup: user=%+v err=%v", user, err)
	}
}
