package api

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestNewLoginLimiter_InMemoryOnSQLite: a SQLite (single-instance) backend keeps
// the in-memory limiter, avoiding a database round trip per login attempt.
func TestNewLoginLimiter_InMemoryOnSQLite(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := dbtest.New(t)
	if _, ok := newLoginLimiter(store, 10, time.Minute).(*keyedRateLimiter); !ok {
		t.Errorf("SQLite login limiter = %T, want *keyedRateLimiter (in-memory)",
			newLoginLimiter(store, 10, time.Minute))
	}
}

// TestNewLoginLimiter_SharedOnPostgres: a Postgres (HA-capable) backend gets the
// database-backed limiter so the count is shared across load-balanced instances.
func TestNewLoginLimiter_SharedOnPostgres(t *testing.T) {
	store, _ := dbtest.NewPostgres(t)
	if _, ok := newLoginLimiter(store, 10, time.Minute).(*dbRateLimiter); !ok {
		t.Errorf("Postgres login limiter = %T, want *dbRateLimiter (shared)",
			newLoginLimiter(store, 10, time.Minute))
	}
}
