package db

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// backfillCorpus is the shared accept/reject corpus for the migration-050
// backfill predicate. want is whether the slug is a legal project slug and so
// must survive into the projects table.
var backfillCorpus = []struct {
	slug string
	want bool
}{
	{"analytics", true},
	{"a", true},
	{"ab", true},
	{"a-b", true},
	{"team-1", true},
	{"0-9", true},
	{strings.Repeat("a", 63), true},
	{strings.Repeat("a", 64), false},
	{"-lead", false},
	{"trail-", false},
	{"Upper", false},
	{"has space", false},
	{"has_underscore", false},
	{"dots.here", false},
	{"emoji-\U0001F600", false},
}

// TestMigration050BackfillPredicateParity runs the SQLite backfill predicate as
// real SQL and the Postgres one as its Go-equivalent regexp over one corpus, so
// a divergence between the two dialect files fails here rather than silently
// dropping or admitting rows on whichever backend is not under test.
func TestMigration050BackfillPredicateParity(t *testing.T) {
	// Four positional placeholders rather than one repeated ?1: the repeated
	// form needs named parameters, and the four-arg form is what was verified
	// against a real modernc.org/sqlite connection over this exact corpus.
	sqlitePred := `SELECT length(?) <= 63 AND ? GLOB '[a-z0-9]*' AND ? NOT GLOB '*[^a-z0-9-]*' AND ? NOT GLOB '*-'`
	pgPred := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck

	for _, tc := range backfillCorpus {
		var got int
		if err := conn.QueryRow(sqlitePred, tc.slug, tc.slug, tc.slug, tc.slug).Scan(&got); err != nil {
			t.Fatalf("sqlite predicate on %q: %v", tc.slug, err)
		}
		if (got == 1) != tc.want {
			t.Errorf("sqlite predicate %q = %v, want %v", tc.slug, got == 1, tc.want)
		}
		if pgPred.MatchString(tc.slug) != tc.want {
			t.Errorf("postgres predicate %q = %v, want %v", tc.slug, pgPred.MatchString(tc.slug), tc.want)
		}
	}

	// The predicates asserted above must be the ones the migrations actually
	// run. A test that validates a copy proves nothing about the shipped SQL.
	sq := mustMigrationSource(t, "sqlite", 50)
	if !strings.Contains(sq, "GLOB '[a-z0-9]*'") || !strings.Contains(sq, "NOT GLOB '*[^a-z0-9-]*'") || !strings.Contains(sq, "NOT GLOB '*-'") {
		t.Error("sqlite 050 must use the GLOB backfill predicate asserted by this test")
	}
	pg := mustMigrationSource(t, "postgres", 50)
	if !strings.Contains(pg, `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) {
		t.Error("postgres 050 must use the anchored regexp backfill predicate asserted by this test")
	}
}

func mustMigrationSource(t *testing.T, dialect string, version int) string {
	t.Helper()
	ms, err := loadMigrations(dialect)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version == version {
			return m.sql
		}
	}
	t.Fatalf("%s migration %d not embedded", dialect, version)
	return ""
}

// migratedThrough returns an in-memory SQLite store whose schema is migrations
// 1..version WITH the ledger recorded, so a later Migrate() applies only what
// comes after. It deliberately does not reuse Store.Migrate for the seed step:
// Migrate takes no stop-at-version argument, and seeding without a ledger trips
// the legacy-adoption branch (db.go:297-307), which runs every embedded
// migration at once - including the one under test, before the fixture rows
// exist. loadMigrations returns versions in strict ascending order (pinned by
// TestLoadMigrationsOrderedAndUnique), so breaking on the first over-version
// entry is safe.
func migratedThrough(t *testing.T, version int) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	ms, err := loadMigrations("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > version {
			break
		}
		if _, err := s.db.Exec(m.sql); err != nil {
			t.Fatalf("seed migration %s: %v", m.name, err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, "seed"); err != nil {
			t.Fatalf("record migration %s: %v", m.name, err)
		}
	}
	return s
}

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// TestMigration050RetiresDefaultSentinel proves the sentinel rewrite runs and
// that a pre-existing named project is preserved as a projects row.
func TestMigration050RetiresDefaultSentinel(t *testing.T) {
	// The migration body under test is the SQLite one (Open(":memory:") selects
	// the sqlite dialect either way); the Postgres predicate's equivalence is
	// covered by the parity test above, not here.
	skipIfPostgres(t)
	s := migratedThrough(t, 49)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('u','x','admin')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, project_slug, owner_id, access) VALUES ('a','A','default',1,'private')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, project_slug, owner_id, access) VALUES ('b','B','analytics',1,'private')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, project_slug, owner_id, access) VALUES ('c','C','Bad Slug',1,'private')`)

	// Applies 050 and anything after it. 050 is the newest migration when this
	// task lands, so this is exactly "migrate to 50".
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM apps WHERE project_slug = 'default'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("migration must rewrite the 'default' sentinel to '', %d rows left", n)
	}
	var slugs []string
	rows, err := s.DB().Query(`SELECT slug FROM projects ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var sl string
		if err := rows.Scan(&sl); err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, sl)
	}
	if len(slugs) != 1 || slugs[0] != "analytics" {
		t.Errorf("projects backfill = %v, want [analytics] (no sentinel, no invalid slug)", slugs)
	}
}
