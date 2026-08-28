package dbtest

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// These tests pin the contract every fixture consumer relies on. New clones a
// template instead of migrating, so each property below is exactly what a
// template clone could silently get wrong: an incomplete ledger, state leaking
// between tests, or connection settings lost in the swap.

func TestNew_IsFullyMigrated(t *testing.T) {
	store := New(t)

	pending, err := store.PendingMigrations()
	if err != nil {
		t.Fatalf("pending migrations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("fixture must be fully migrated; pending = %v", pending)
	}
	got, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	want, err := db.LatestSchemaVersion()
	if err != nil {
		t.Fatalf("latest schema version: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want latest embedded %d", got, want)
	}
	// Migrate on a clone is a no-op, which is what production code paths that
	// call it unconditionally (server boot) expect of a current database.
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate a fully migrated fixture: %v", err)
	}
}

func TestNew_EachCallIsIsolated(t *testing.T) {
	first := New(t)
	if err := first.CreateUser(db.CreateUserParams{Username: "leak-probe", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatalf("seed first store: %v", err)
	}

	// Opened after the write: a template that shared state with its clones, or
	// a clone that wrote back into it, would show the row here.
	second := New(t)
	if _, err := second.GetUserByUsername("leak-probe"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("row written to one fixture visible in another: err = %v", err)
	}
	if _, err := first.GetUserByUsername("leak-probe"); err != nil {
		t.Fatalf("row missing from the store that wrote it: %v", err)
	}
}

func TestNew_ForeignKeysEnforced(t *testing.T) {
	store := New(t)

	// app_members references apps(slug) and users(id); neither target exists.
	_, err := store.DB().Exec(`INSERT INTO app_members (app_slug, user_id) VALUES ('no-such-app', 424242)`)
	if err == nil {
		t.Fatal("insert violating a foreign key succeeded; constraints are not enforced on the fixture")
	}
	if os.Getenv(dsnEnv) != "" {
		return
	}
	// foreign_keys is a per-connection SQLite pragma, not part of the database
	// image, so check it survived the template swap by name as well.
	var fk int
	if err := store.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys pragma = %d after loading the template, want 1", fk)
	}
}

// TestNew_PostgresTemplateRenamedIntoPlace proves the template build finished
// through its rename step: exactly one template exists for this binary's
// migration set and no work-in-progress database from this process is left
// behind. Skips unless SHINYHUB_TEST_POSTGRES_DSN is set.
func TestNew_PostgresTemplateRenamedIntoPlace(t *testing.T) {
	RequirePostgres(t)
	store := New(t)

	digest, err := db.MigrationsDigest()
	if err != nil {
		t.Fatalf("migrations digest: %v", err)
	}
	prefix := "shtest_tpl_" + digest[:16]
	rows, err := store.DB().Query(`SELECT datname FROM pg_database WHERE datname LIKE $1`, prefix+"%")
	if err != nil {
		t.Fatalf("list template databases: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// Another test process may be mid-build under its own pid; only this
	// process's leftovers prove a failure here.
	for _, name := range names {
		if strings.HasSuffix(name, "_wip_"+strconv.Itoa(os.Getpid())) {
			t.Fatalf("work-in-progress template %s was not renamed into place", name)
		}
	}
	var final int
	for _, name := range names {
		if name == prefix {
			final++
		}
	}
	if final != 1 {
		t.Fatalf("template databases matching %s: %v, want exactly %s", prefix, names, prefix)
	}
}

// BenchmarkNew is the fixture cost itself, the number that made the race jobs
// slow. Run with and without -race; both should sit in the low milliseconds.
func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		New(b)
	}
}
