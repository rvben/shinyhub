package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestReplicaCrashDiagnosticsSurviveRestart(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	code := 137
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "crashed", ExitCode: &code,
		Reason: "out of memory",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Unix(1_777_000_000, 0).UTC()
	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Signal: "SIGKILL", Reason: "kernel OOM-killed replica",
		ExitObservedAt: observedAt, ExitOOMKilled: true, ExitRunID: "run-2",
	}); err != nil {
		t.Fatal(err)
	}
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replicas) != 1 {
		t.Fatalf("replicas = %d, want 1", len(replicas))
	}
	rep := replicas[0]
	if rep.Status != "crashed" || rep.Signal != "SIGKILL" || rep.Reason != "kernel OOM-killed replica" {
		t.Errorf("diagnostic = status %q signal %q reason %q", rep.Status, rep.Signal, rep.Reason)
	}
	if rep.ExitCode != nil {
		t.Errorf("new signal exit retained stale exit code %v", *rep.ExitCode)
	}
	if rep.RestartCount != 2 {
		t.Errorf("restart_count = %d, want 2", rep.RestartCount)
	}
	if rep.LastExit == nil || rep.LastExit.ObservedAt == nil || !rep.LastExit.ObservedAt.Equal(observedAt) {
		t.Fatalf("last_exit timestamp = %+v, want %s", rep.LastExit, observedAt)
	}
	if !rep.LastExit.OOMKilled || rep.LastExit.RunID != "run-2" || rep.LastExit.CrashCount != 2 {
		t.Errorf("last_exit = %+v", rep.LastExit)
	}
}
