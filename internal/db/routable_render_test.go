package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestListRoutableReplicas_CarriesRenderSeconds verifies that a routable
// replica row carries the parent app's render_seconds value so the pool
// syncer can reconcile pacing without a second query.
func TestListRoutableReplicas_CarriesRenderSeconds(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "demo", owner.ID)
	if err := store.SetAppRenderSeconds(app.ID, 1.3); err != nil {
		t.Fatalf("SetAppRenderSeconds: %v", err)
	}

	// A running replica makes the app routable.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:       app.ID,
		Index:       0,
		Status:      db.ReplicaStatusRunning,
		EndpointURL: "http://192.0.2.1:9000",
		Provider:    "fargate",
		Tier:        "fargate",
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}

	rows, err := store.ListRoutableReplicas()
	if err != nil {
		t.Fatalf("ListRoutableReplicas: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d routable rows, want 1", len(rows))
	}
	if rows[0].AppRenderSeconds != 1.3 {
		t.Fatalf("AppRenderSeconds = %v, want 1.3", rows[0].AppRenderSeconds)
	}
}
