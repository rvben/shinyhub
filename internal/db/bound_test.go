package db

import (
	"context"
	"testing"
)

// TestBoundConnFailedCommitReleasesCleanConnection covers the SQLite write
// handle when COMMIT itself fails (here: the transaction's context is already
// cancelled, which the driver reports before running the statement). The
// dedicated connection goes back to the pool, so the open transaction must be
// rolled back first or the next borrower inherits it and its own BEGIN
// IMMEDIATE fails with "cannot start a transaction within a transaction". An
// in-memory store has a pool of exactly one connection, so the second
// beginImmediateSQLite below is guaranteed to reuse the connection.
func TestBoundConnFailedCommitReleasesCleanConnection(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := store.rawDB()

	ctx, cancel := context.WithCancel(context.Background())
	tx, err := beginImmediateSQLite(ctx, raw, sqliteDialect{})
	if err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE bound_probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("write inside transaction: %v", err)
	}
	cancel()
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit under a cancelled context must fail")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after a failed Commit = %v, want nil", err)
	}

	next, err := beginImmediateSQLite(context.Background(), raw, sqliteDialect{})
	if err != nil {
		t.Fatalf("BEGIN IMMEDIATE on the reused connection: %v", err)
	}
	defer func() { _ = next.Rollback() }()
	var n int
	if err := next.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'bound_probe'`).Scan(&n); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if n != 0 {
		t.Fatalf("the aborted transaction's table is visible; the failed COMMIT was not rolled back")
	}
}

// TestBoundConnRollbackUnderCancelledContextStillRollsBack covers the usual
// error path: the caller's deferred Rollback runs after the transaction's
// context was cancelled. ROLLBACK must still execute, otherwise the pooled
// connection keeps the open transaction exactly as in the failed-COMMIT case.
func TestBoundConnRollbackUnderCancelledContextStillRollsBack(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := store.rawDB()

	ctx, cancel := context.WithCancel(context.Background())
	tx, err := beginImmediateSQLite(ctx, raw, sqliteDialect{})
	if err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE bound_probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("write inside transaction: %v", err)
	}
	cancel()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback under a cancelled context = %v, want nil", err)
	}

	next, err := beginImmediateSQLite(context.Background(), raw, sqliteDialect{})
	if err != nil {
		t.Fatalf("BEGIN IMMEDIATE on the reused connection: %v", err)
	}
	defer func() { _ = next.Rollback() }()
	var n int
	if err := next.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'bound_probe'`).Scan(&n); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if n != 0 {
		t.Fatalf("the rolled-back transaction's table is visible")
	}
}
