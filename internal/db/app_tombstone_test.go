package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// The 'deleting' tombstone is terminal: a status change written after it,
// audited or not, is refused and leaves the tombstone in place, so startup
// reconciliation still finds the app and finishes its deletion.
func TestUpdateAppStatus_NeverLeavesTheDeletingTombstone(t *testing.T) {
	for name, p := range map[string]db.UpdateAppStatusParams{
		"plain":   {Slug: "demo", Status: "running"},
		"stop":    {Slug: "demo", Status: "stopped", OperatorStopped: true},
		"audited": {Slug: "demo", Status: "running", Reason: "restart"},
	} {
		t.Run(name, func(t *testing.T) {
			store := mustOpenDB(t)
			u := mustCreateUser(t, store, "owner", "developer")
			mustCreateApp(t, store, "demo", u.ID)
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "deleting"}); err != nil {
				t.Fatal(err)
			}

			if err := store.UpdateAppStatus(p); !errors.Is(err, db.ErrAppDeleting) {
				t.Fatalf("status change of a tombstoned app = %v, want ErrAppDeleting", err)
			}
			got, err := store.GetApp("demo")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "deleting" {
				t.Fatalf("status after the refused change = %q, want the tombstone kept", got.Status)
			}
		})
	}
}

// Writing the tombstone again (a retried deletion) succeeds, and a missing app
// is still not found rather than reported as being deleted.
func TestUpdateAppStatus_TombstoneRetryAndMissingApp(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	for i := 0; i < 2; i++ {
		if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "deleting"}); err != nil {
			t.Fatalf("tombstone write %d: %v", i+1, err)
		}
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "absent", Status: "running"}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("status change of a missing app = %v, want ErrNotFound", err)
	}
}
