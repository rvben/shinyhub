package backup

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
)

// pgConnEnv splits a libpq URI DSN into a password-free DSN that is safe to pass
// in argv and the process environment carrying the password via PGPASSWORD, so
// the credential never appears in a process listing. A non-URI or password-less
// DSN passes through unchanged with the inherited environment.
func pgConnEnv(dsn string) (cleanDSN string, env []string) {
	env = os.Environ()
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn, env
	}
	pw, ok := u.User.Password()
	if !ok {
		return dsn, env
	}
	env = append(env, "PGPASSWORD="+pw)
	u.User = url.User(u.User.Username())
	return u.String(), env
}

// pgDump writes a custom-format (pg_dump --format=custom) archive of the
// database named by dsn to destPath. Custom format is compressed and is what
// pg_restore --clean consumes. --no-owner / --no-privileges keep the dump
// portable across roles so it restores cleanly into a database owned by a
// different user. pg_dump is a consistent online snapshot (it runs in a single
// transaction), so this is safe against a live server, mirroring SQLite's
// VACUUM INTO. The output holds full database contents, so it is forced to
// owner-only regardless of the process umask.
func pgDump(dsn, destPath string) error {
	cleanDSN, env := pgConnEnv(dsn)
	cmd := exec.Command("pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--file", destPath, cleanDSN)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w%s", err, stderrTail(&stderr))
	}
	if err := os.Chmod(destPath, 0o600); err != nil {
		return fmt.Errorf("secure dump %s: %w", destPath, err)
	}
	return nil
}

// pgRestore loads a custom-format archive into the database named by dsn,
// dropping any pre-existing objects first (--clean --if-exists) so the target
// becomes an exact copy. --no-owner / --no-privileges ignore the dump's
// ownership so it lands under the connecting role. The target database must
// already exist (pg_restore restores into a database, it does not create one).
func pgRestore(dsn, srcPath string) error {
	cleanDSN, env := pgConnEnv(dsn)
	cmd := exec.Command("pg_restore", "--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--dbname", cleanDSN, srcPath)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w%s", err, stderrTail(&stderr))
	}
	return nil
}

// stderrTail returns the captured stderr prefixed with ": " for error context,
// or "" when empty, so callers can append it without producing a dangling colon.
func stderrTail(b *bytes.Buffer) string {
	s := bytes.TrimSpace(b.Bytes())
	if len(s) == 0 {
		return ""
	}
	return ": " + string(s)
}
