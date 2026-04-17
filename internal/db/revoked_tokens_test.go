package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestRevokeAndCheckToken(t *testing.T) {
	store := mustOpenDB(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "alice",
		PasswordHash: "hash",
		Role:         "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, _ := store.GetUserByUsername("alice")

	revoked, err := store.IsTokenRevoked("never-issued")
	if err != nil {
		t.Fatalf("pre-revoke lookup: %v", err)
	}
	if revoked {
		t.Fatal("expected unknown jti to report not revoked")
	}

	jti := "jti-abc-123"
	if err := store.RevokeToken(jti, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	revoked, err = store.IsTokenRevoked(jti)
	if err != nil {
		t.Fatalf("post-revoke lookup: %v", err)
	}
	if !revoked {
		t.Error("expected revoked jti to report revoked")
	}
}

func TestRevokeToken_IdempotentInsert(t *testing.T) {
	store := mustOpenDB(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")

	jti := "duplicate-jti"
	exp := time.Now().Add(time.Hour)
	if err := store.RevokeToken(jti, u.ID, exp); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := store.RevokeToken(jti, u.ID, exp); err != nil {
		t.Fatalf("second revoke should be idempotent: %v", err)
	}
}

func TestIsTokenRevoked_IgnoresExpiredEntries(t *testing.T) {
	store := mustOpenDB(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")

	jti := "stale-jti"
	// Insert a revocation whose expiry is already in the past. The next
	// RevokeToken call will prune it via the sweep; meanwhile IsTokenRevoked
	// must treat it as not-revoked because a token past its signed expiry
	// cannot be used anyway.
	if _, err := store.DB().Exec(
		`INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES (?, ?, ?)`,
		jti, u.ID, time.Now().Add(-time.Hour).Unix(),
	); err != nil {
		t.Fatalf("seed expired revocation: %v", err)
	}

	revoked, err := store.IsTokenRevoked(jti)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if revoked {
		t.Error("expected expired revocation to be ignored")
	}
}

func TestRevokeToken_PrunesExpiredEntries(t *testing.T) {
	store := mustOpenDB(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")

	// Seed two expired rows and one live row.
	seed := func(jti string, offset time.Duration) {
		t.Helper()
		if _, err := store.DB().Exec(
			`INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES (?, ?, ?)`,
			jti, u.ID, time.Now().Add(offset).Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}
	seed("old-1", -2*time.Hour)
	seed("old-2", -time.Minute)
	seed("live", time.Hour)

	// A fresh RevokeToken should prune the expired rows.
	if err := store.RevokeToken("new-jti", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM revoked_tokens`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	// live + new-jti; old-1 and old-2 should be gone.
	if count != 2 {
		t.Errorf("expected 2 live rows after prune, got %d", count)
	}
}
