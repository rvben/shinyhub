package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestAppsAPI_HealthyReplicaSeparatesCurrentStateFromLastExit(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	observedAt := time.Unix(1_777_000_000, 0).UTC()
	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Signal: "SIGKILL", Reason: "kernel OOM-killed replica",
		ExitObservedAt: observedAt, ExitOOMKilled: true, ExitRunID: "run-dead",
	}); err != nil {
		t.Fatal(err)
	}
	pid, port := 1234, 5678
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
		Provider: "native", Tier: "local",
	}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/apps/demo", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		App            db.App        `json:"app"`
		ReplicasStatus []*db.Replica `json:"replicas_status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.App.LastReplicaError != "" {
		t.Errorf("healthy app last_replica_error = %q, want empty", body.App.LastReplicaError)
	}
	if body.App.LastReplicaExit == nil || !body.App.LastReplicaExit.OOMKilled {
		t.Fatalf("app last_replica_exit = %+v", body.App.LastReplicaExit)
	}
	if len(body.ReplicasStatus) != 1 {
		t.Fatalf("replicas = %d", len(body.ReplicasStatus))
	}
	rep := body.ReplicasStatus[0]
	if rep.Status != "running" || rep.Reason != "" || rep.ExitCode != nil || rep.Signal != "" || rep.RestartCount != 0 {
		t.Errorf("healthy current fields are stale: %+v", rep)
	}
	if rep.LastExit == nil || rep.LastExit.RunID != "run-dead" || !rep.LastExit.OOMKilled || rep.LastExit.Signal != "SIGKILL" {
		t.Errorf("historical last_exit = %+v", rep.LastExit)
	}
	// Canonical exit_* names live in the historical object after recovery; the
	// flat current-process aliases are intentionally empty on a healthy replica.
	if rep.LastExit.Reason != "kernel OOM-killed replica" || rep.ExitSignal != "" || rep.ExitReason != "" {
		t.Errorf("canonical exit fields = current %q/%q last %+v", rep.ExitSignal, rep.ExitReason, rep.LastExit)
	}
}
