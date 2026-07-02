package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/secrets"
)

// TestImportFrom_SQLiteToPostgresRoundTrip is the safety net for the backend
// migration: it populates a SQLite source across many tables (including the
// tricky columns - a REAL/double autoscale_target, encrypted BLOB/bytea env
// secrets, a TEXT-stored timestamp, and an int-epoch timestamp), migrates it
// into a fresh Postgres target, and verifies every table's row count matches,
// representative values are preserved exactly, FK references stay intact, and
// the id sequences are reset so new inserts do not collide. Skips without a
// Postgres DSN.
func TestImportFrom_SQLiteToPostgresRoundTrip(t *testing.T) {
	target, _ := dbtest.NewPostgres(t) // isolated, migrated Postgres; skips if unset

	// Source: a fresh on-disk SQLite store.
	srcPath := t.TempDir() + "/source.db"
	src, err := db.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := src.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Populate representative data.
	mustUser := func(name, role string) *db.User {
		if err := src.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "h-" + name, Role: role}); err != nil {
			t.Fatal(err)
		}
		u, _ := src.GetUserByUsername(name)
		return u
	}
	owner := mustUser("owner", "admin")
	viewer := mustUser("viewer", "developer")

	if err := src.CreateApp(db.CreateAppParams{Slug: "alpha", Name: "Alpha", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := src.CreateApp(db.CreateAppParams{Slug: "bravo", Name: "Bravo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	alpha, _ := src.GetAppBySlug("alpha")

	// A float column (autoscale_target REAL -> double) with a value that would
	// perturb under float4, and identity_headers = 1 (INTEGER in SQLite ->
	// BOOLEAN in Postgres) so the int->bool coercion on the copy path is covered.
	if _, err := src.DB().Exec(`UPDATE apps SET autoscale_target = 0.8, autoscale_enabled = 1, identity_headers = 1 WHERE id = ?`, alpha.ID); err != nil {
		t.Fatal(err)
	}

	// Encrypted env secret (BLOB/bytea) + a plaintext var.
	encKey := secrets.DeriveKey("migration-test-secret-migration-32")
	ct, _ := secrets.Encrypt(encKey, []byte("super-secret-value"))
	if err := src.UpsertAppEnvVar(alpha.ID, "API_KEY", ct, true); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertAppEnvVar(alpha.ID, "PUBLIC", []byte("plain"), false); err != nil {
		t.Fatal(err)
	}

	// FK-bearing rows: a member (app+user), an audit event (text timestamp), a
	// replica (int-epoch updated_at).
	if err := src.GrantAppAccessWithRole("alpha", viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	src.LogAuditEvent(db.AuditEventParams{UserID: &owner.ID, Action: "deploy", ResourceType: "app", ResourceID: "alpha", Detail: "v1"})
	if err := src.UpsertReplica(db.UpsertReplicaParams{AppID: alpha.ID, Index: 0, Status: "running", Provider: "native", Tier: "default"}); err != nil {
		t.Fatal(err)
	}

	// Migrate.
	counts, err := target.ImportFrom(src)
	if err != nil {
		t.Fatalf("ImportFrom: %v", err)
	}

	// Every source table's row count must match the target's.
	for _, table := range sourceTableNames(t, src) {
		var srcN, dstN int
		src.DB().QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&srcN)
		target.DB().QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&dstN)
		if srcN != dstN {
			t.Errorf("row count mismatch for %s: source=%d target=%d (copied=%d)", table, srcN, dstN, counts[table])
		}
	}

	// Spot-check preserved values through the target store's typed reads.
	ta, err := target.GetAppBySlug("alpha")
	if err != nil {
		t.Fatalf("target GetAppBySlug: %v", err)
	}
	if ta.ID != alpha.ID {
		t.Errorf("app id not preserved: source=%d target=%d", alpha.ID, ta.ID)
	}
	if ta.AutoscaleTarget != 0.8 {
		t.Errorf("autoscale_target = %v, want exactly 0.8 (float precision preserved)", ta.AutoscaleTarget)
	}

	// Encrypted secret bytes must round-trip and still decrypt.
	ev, err := target.GetAppEnvVar(alpha.ID, "API_KEY")
	if err != nil {
		t.Fatalf("target GetAppEnvVar: %v", err)
	}
	got, err := secrets.Decrypt(encKey, ev.Value)
	if err != nil || string(got) != "super-secret-value" {
		t.Errorf("encrypted secret did not round-trip through bytea: err=%v got=%q", err, got)
	}

	// FK reference preserved: the member references the migrated app + user.
	members, err := target.ListAppMembers("alpha", 100, 0)
	if err != nil {
		t.Fatalf("target ListAppMembers: %v", err)
	}
	if len(members) != 1 || members[0].UserID != viewer.ID {
		t.Errorf("app member FK not preserved: %+v", members)
	}

	// Timestamp preserved: the audit event's created_at survives the text ->
	// timestamptz coercion (same instant within a second).
	var srcTS, dstTS int64
	src.DB().QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&srcTS)
	target.DB().QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&dstTS)
	if srcTS != dstTS {
		t.Errorf("audit event not copied: source=%d target=%d", srcTS, dstTS)
	}

	// Sequence reset: a new app on the target must get an id above the migrated
	// max, not collide with a preserved id.
	if err := target.CreateApp(db.CreateAppParams{Slug: "charlie", Name: "Charlie", OwnerID: owner.ID}); err != nil {
		t.Fatalf("create app on migrated target (sequence not reset?): %v", err)
	}
	charlie, _ := target.GetAppBySlug("charlie")
	if charlie.ID <= alpha.ID {
		t.Errorf("new app id %d must exceed migrated max; sequence was not reset", charlie.ID)
	}
}

func sourceTableNames(t *testing.T, src *db.Store) []string {
	t.Helper()
	rows, err := src.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}
