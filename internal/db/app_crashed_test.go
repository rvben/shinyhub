package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// A freshly created app carries no crash diagnostic.
func TestApp_CrashFieldsDefaultEmpty(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if app.LastError != "" || app.CrashedAt != 0 {
		t.Fatalf("new app crash fields = (%q, %d), want empty/0", app.LastError, app.CrashedAt)
	}
}

// MarkAppCrashed sets status=crashed and records the reason + a timestamp, and
// GetApp round-trips them (scanApp reads the new columns).
func TestMarkAppCrashed_RoundTrip(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	const reason = "boot failed: ModuleNotFoundError: No module named 'pandas'\n  File \"app.py\", line 3"
	if err := store.MarkAppCrashed("demo", reason); err != nil {
		t.Fatalf("MarkAppCrashed: %v", err)
	}

	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.Status != "crashed" {
		t.Fatalf("status = %q, want crashed", got.Status)
	}
	if got.LastError != reason {
		t.Fatalf("last_error = %q, want %q", got.LastError, reason)
	}
	if got.CrashedAt <= 0 {
		t.Fatalf("crashed_at = %d, want a positive epoch", got.CrashedAt)
	}
}

// Any transition out of crashed (here: back to running) clears the diagnostic,
// so a recovered app never shows a stale crash reason.
func TestUpdateAppStatus_ClearsCrashDiagnostic(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	if err := store.MarkAppCrashed("demo", "some traceback"); err != nil {
		t.Fatalf("MarkAppCrashed: %v", err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "running"}); err != nil {
		t.Fatalf("UpdateAppStatus: %v", err)
	}

	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.LastError != "" || got.CrashedAt != 0 {
		t.Fatalf("crash fields not cleared: (%q, %d)", got.LastError, got.CrashedAt)
	}
}

// A bare status write to "crashed" must preserve an already-recorded reason
// (crashed transitions go through MarkAppCrashed; UpdateAppStatus must not wipe it).
func TestUpdateAppStatus_CrashedPreservesReason(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	if err := store.MarkAppCrashed("demo", "the real reason"); err != nil {
		t.Fatalf("MarkAppCrashed: %v", err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "crashed"}); err != nil {
		t.Fatalf("UpdateAppStatus: %v", err)
	}

	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.LastError != "the real reason" {
		t.Fatalf("last_error = %q, want it preserved on a status-write to crashed", got.LastError)
	}
}

// A delete in flight must win: MarkAppCrashed never resurrects a deleting app.
func TestMarkAppCrashed_SkipsDeletingApp(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "deleting"}); err != nil {
		t.Fatalf("UpdateAppStatus(deleting): %v", err)
	}
	// The guard matches no rows; the store reports it as not-found rather than
	// silently flipping a deleting app to crashed.
	if err := store.MarkAppCrashed("demo", "late crash"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("MarkAppCrashed on deleting app: err = %v, want ErrNotFound", err)
	}

	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.Status != "deleting" {
		t.Fatalf("status = %q, want deleting (unchanged)", got.Status)
	}
}
