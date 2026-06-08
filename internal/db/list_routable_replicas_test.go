package db_test

import (
	"fmt"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestListRoutableReplicas verifies the query that feeds the clustered pool
// syncer. Key invariants:
//
//   - Only replicas with status 'running' or 'draining' are returned.
//   - Replicas belonging to a 'degraded' parent app are still returned; the
//     pool syncer sources from replica.status, not app.status.
//   - 'lost' and 'stopped' replicas are excluded.
//   - Each row carries the slug and max_sessions_per_replica from the parent
//     app so the syncer can configure the pool cap without a second query.
func TestListRoutableReplicas(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")

	appA := mustCreateApp(t, store, "app-a", owner.ID)
	appB := mustCreateApp(t, store, "app-b", owner.ID)

	seed := func(appID int64, idx int, status, desiredState string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        appID,
			Index:        idx,
			Status:       status,
			DesiredState: desiredState,
			Provider:     "fargate",
			Tier:         "fargate",
			EndpointURL:  fmt.Sprintf("http://192.0.2.%d:9000", idx+1),
		}); err != nil {
			t.Fatalf("seed replica (app=%d idx=%d): %v", appID, idx, err)
		}
	}

	// app-a: one running, one lost (lost must be excluded).
	seed(appA.ID, 0, db.ReplicaStatusRunning, "running")
	seed(appA.ID, 1, db.ReplicaStatusLost, "running")

	// app-b: one running, one with desired_state='draining' but status
	// still running (draining is a desired-state signal, not a status value).
	seed(appB.ID, 0, db.ReplicaStatusRunning, "draining")
	seed(appB.ID, 1, db.ReplicaStatusRunning, "running")

	rows, err := store.ListRoutableReplicas()
	if err != nil {
		t.Fatalf("ListRoutableReplicas: %v", err)
	}

	// Expect exactly 3 rows: app-a/0, app-b/0, app-b/1
	if len(rows) != 3 {
		t.Fatalf("expected 3 routable replicas, got %d: %+v", len(rows), rows)
	}
	for _, rr := range rows {
		if rr.Replica.Status != db.ReplicaStatusRunning {
			t.Errorf("expected running replica in routable set, got status=%q for %s#%d",
				rr.Replica.Status, rr.Slug, rr.Replica.Index)
		}
		if rr.Slug != "app-a" && rr.Slug != "app-b" {
			t.Errorf("unexpected slug %q", rr.Slug)
		}
	}
}

// TestListRoutableReplicas_DegradedAppIncluded is the regression guard for the
// most critical invariant: a degraded parent app must NOT cause its running
// replicas to disappear from the routable set. The pool syncer sources from
// replica.status, not app.status.
func TestListRoutableReplicas_DegradedAppIncluded(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "degraded-app", owner.ID)

	// Mark the app degraded (e.g. one replica crashed).
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   "degraded-app",
		Status: "degraded",
	}); err != nil {
		t.Fatalf("UpdateAppStatus: %v", err)
	}

	// One replica is still running.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:       app.ID,
		Index:       0,
		Status:      db.ReplicaStatusRunning,
		EndpointURL: "http://192.0.2.10:9000",
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
		t.Fatalf("expected 1 routable replica for degraded app, got %d", len(rows))
	}
	if rows[0].Slug != "degraded-app" {
		t.Errorf("slug = %q, want degraded-app", rows[0].Slug)
	}
	if rows[0].Replica.Status != db.ReplicaStatusRunning {
		t.Errorf("replica status = %q, want running", rows[0].Replica.Status)
	}
}

// TestListRoutableReplicas_ExcludesLostAndStopped confirms that lost replicas
// (and any future stopped status) are excluded from the routable set.
func TestListRoutableReplicas_ExcludesLostAndStopped(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app-z", owner.ID)

	seed := func(idx int, status string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID:       app.ID,
			Index:       idx,
			Status:      status,
			EndpointURL: "http://192.0.2.1:9000",
			Provider:    "fargate",
			Tier:        "fargate",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	seed(0, db.ReplicaStatusLost)
	seed(1, "stopped")

	rows, err := store.ListRoutableReplicas()
	if err != nil {
		t.Fatalf("ListRoutableReplicas: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 routable replicas for lost/stopped, got %d", len(rows))
	}
}

// TestListRoutableReplicas_CarriesPoolCap asserts that the row carries
// max_sessions_per_replica from the parent app row, not a fixed default.
func TestListRoutableReplicas_CarriesPoolCap(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "capped-app", owner.ID)

	// Set max_sessions_per_replica to a recognisable value.
	if _, err := store.DB().Exec(
		`UPDATE apps SET max_sessions_per_replica = 7 WHERE id = ?`, app.ID,
	); err != nil {
		t.Fatalf("set max_sessions: %v", err)
	}

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
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].AppMaxSessionsPerRepl != 7 {
		t.Errorf("AppMaxSessionsPerRepl = %d, want 7", rows[0].AppMaxSessionsPerRepl)
	}
}
