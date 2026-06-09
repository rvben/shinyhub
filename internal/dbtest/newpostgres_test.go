package dbtest

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestNewPostgres_DSNReachesSameDatabase proves the DSN NewPostgres returns
// points at the SAME isolated database as the returned store: a row written via
// the store is visible to a second, independently opened connection on that DSN
// (which is exactly what the kill-the-active child processes rely on).
// Skips unless SHINYHUB_TEST_POSTGRES_DSN is set.
func TestNewPostgres_DSNReachesSameDatabase(t *testing.T) {
	store, dsn := NewPostgres(t)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "dsn-probe", PasswordHash: "x", Role: "admin",
	}); err != nil {
		t.Fatalf("seed via store: %v", err)
	}

	second, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open second connection on returned dsn: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if _, err := second.GetUserByUsername("dsn-probe"); err != nil {
		t.Fatalf("row seeded via store not visible on returned dsn: %v", err)
	}
}
