package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// TestPruneDevelopmentSessions deletes only ended, non-ephemeral sessions
// past retention. An active session and an ephemeral session must survive
// regardless of age: EndStaleDevelopmentSessions never touches ephemeral
// rows, and ephemeral development_sessions rows are removed automatically
// when their app is deleted (app_id cascades), so pruning them here would
// race that path for no benefit.
func TestPruneDevelopmentSessions(t *testing.T) {
	store := openTestStore(t)
	owner := mustCreateUser(t, store, "dev-session-owner", "developer")
	existingApp := mustCreateApp(t, store, "dev-session-existing", owner.ID)

	// An ancient ended session on an ordinary ("existing") app: eligible.
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: "sess-old", AppID: existingApp.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatalf("upsert old session: %v", err)
	}
	if err := store.EndDevelopmentSession(existingApp.ID, "sess-old", time.Now().UTC().Add(-100*24*time.Hour)); err != nil {
		t.Fatalf("end old session: %v", err)
	}

	// A recently ended session: not yet past retention.
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: "sess-fresh", AppID: existingApp.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatalf("upsert fresh session: %v", err)
	}
	if err := store.EndDevelopmentSession(existingApp.ID, "sess-fresh", time.Now().UTC()); err != nil {
		t.Fatalf("end fresh session: %v", err)
	}

	// A still-active session: never eligible, no matter how old created_at is.
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: "sess-active", AppID: existingApp.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatalf("upsert active session: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE development_sessions SET created_at = ?, updated_at = ?
		WHERE id = 'sess-active'`, "2000-01-01 00:00:00", "2000-01-01 00:00:00"); err != nil {
		t.Fatalf("backdate active session: %v", err)
	}

	// An ancient ended ephemeral session on a still-live ephemeral app: never
	// eligible even past retention, since it did not go through the
	// app-deletion cascade that normally removes ephemeral session rows.
	expires := time.Now().UTC().Add(time.Hour)
	if _, err := store.CreateDevelopmentApp(db.CreateDevelopmentAppParams{
		App: db.CreateAppParams{Slug: "dev-session-ephemeral", Name: "dev-session-ephemeral", OwnerID: owner.ID, Access: "private"},
		Session: db.UpsertDevelopmentSessionParams{
			ID: "sess-ephemeral", TargetKind: db.DevelopmentTargetEphemeral, ExpiresAt: &expires,
		},
	}); err != nil {
		t.Fatalf("create ephemeral app: %v", err)
	}
	ephemeralApp, err := store.GetAppBySlug("dev-session-ephemeral")
	if err != nil {
		t.Fatalf("get ephemeral app: %v", err)
	}
	if err := store.EndDevelopmentSession(ephemeralApp.ID, "sess-ephemeral", time.Now().UTC().Add(-100*24*time.Hour)); err != nil {
		t.Fatalf("end ephemeral session: %v", err)
	}

	// A zero retention is a no-op: nothing is deleted.
	if n, err := store.PruneDevelopmentSessions(0); err != nil || n != 0 {
		t.Fatalf("PruneDevelopmentSessions(0) = (%d, %v), want (0, nil)", n, err)
	}

	deleted, err := store.PruneDevelopmentSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneDevelopmentSessions: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneDevelopmentSessions deleted %d, want 1 (only sess-old)", deleted)
	}
	if _, err := store.GetDevelopmentSession(existingApp.ID, "sess-old"); err != db.ErrNotFound {
		t.Errorf("expected sess-old to be pruned, got %v", err)
	}
	for _, id := range []string{"sess-fresh", "sess-active"} {
		if _, err := store.GetDevelopmentSession(existingApp.ID, id); err != nil {
			t.Errorf("expected %s to survive prune, got %v", id, err)
		}
	}
	if _, err := store.GetDevelopmentSession(ephemeralApp.ID, "sess-ephemeral"); err != nil {
		t.Errorf("expected sess-ephemeral to survive prune, got %v", err)
	}
}
