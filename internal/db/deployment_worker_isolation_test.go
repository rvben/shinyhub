package db_test

import (
	"github.com/rvben/shinyhub/internal/db"
	"testing"
)

func TestDeploymentWorkerIsolationOwnershipAndReplacement(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "policy-owner", "developer")
	app := mustCreateApp(t, s, "policy", owner.ID)
	dep, err := s.BeginDeployment(app.ID, "v1", "/tmp/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetDeploymentWorkerIsolation(dep.ID); err != nil || got != "" {
		t.Fatalf("legacy=%q %v", got, err)
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID+1, dep.ID, "grouped"); err == nil {
		t.Fatal("accepted wrong owner")
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "invalid"); err == nil {
		t.Fatal("accepted invalid policy")
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "grouped"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "multiplex"); err == nil {
		t.Fatal("overwrote live policy")
	}
	if err := s.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: dep.ID, Index: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStoppedDeploymentWorkerIsolation(app.ID, dep.ID, "multiplex"); err == nil {
		t.Fatal("replaced recorded workers")
	}
	if err := s.DeleteDeploymentReplicas(dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStoppedDeploymentWorkerIsolation(app.ID, dep.ID, "multiplex"); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	app.WorkerIsolation = "grouped"
	if got, err := s.ServingWorkerIsolation(app, "grouped"); err != nil || got != "multiplex" {
		t.Fatalf("serving=%q %v", got, err)
	}
}

func TestCrossIsolationPromotionAndCompensationProjection(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "projection-owner", "developer")
	app := mustCreateApp(t, s, "projection", owner.ID)
	old, err := s.BeginDeployment(app.ID, "v1", "/tmp/v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, old.ID, "multiplex"); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteDeployment(old.ID); err != nil {
		t.Fatal(err)
	}
	pid, port := 42, 4042
	if err := s.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running", Provider: "native", DeploymentID: &old.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: old.ID, Index: 0, PID: &pid, Port: &port, Status: "running", Provider: "native"}); err != nil {
		t.Fatal(err)
	}
	next, err := s.BeginDeployment(app.ID, "v2", "/tmp/v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, next.ID, "grouped"); err != nil {
		t.Fatal(err)
	}
	after := *app
	after.WorkerIsolation = "grouped"
	after.WorkerGroupedSize = 4
	after.WorkerWarmSpares = 2
	after.WorkerMaxWorkers = 8
	after.WorkerMaxSessionLifetimeSecs = 60
	if err := s.StageHandoffSettings(next.ID, app, &after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteDeployment(next.ID); err != nil {
		t.Fatal(err)
	}
	reps, err := s.ListReplicas(app.ID)
	if err != nil || len(reps) != 0 {
		t.Fatalf("projection=%v %v", reps, err)
	}
	active, err := s.GetAppByID(app.ID)
	if err != nil || active.WorkerIsolation != "grouped" || active.WorkerWarmSpares != 2 {
		t.Fatalf("settings=%+v %v", active, err)
	}
	if _, err := s.DB().Exec("UPDATE apps SET worker_max_workers=9 WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertDeploymentActivation(next.ID, old.ID, "publish failed"); err != nil {
		t.Fatal(err)
	}
	reps, err = s.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 || reps[0].DeploymentID == nil || *reps[0].DeploymentID != old.ID {
		t.Fatalf("restored projection=%v %v", reps, err)
	}
	active, err = s.GetAppByID(app.ID)
	if err != nil || active.WorkerIsolation != "multiplex" || active.WorkerMaxWorkers != 9 {
		t.Fatalf("compensated settings=%+v %v", active, err)
	}
}

func TestServingWorkerIsolationIgnoresUnpublishedCandidate(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "unpublished-policy-owner", "developer")
	app := mustCreateApp(t, s, "unpublished-policy", owner.ID)
	candidate, err := s.BeginDeployment(app.ID, "candidate", "/tmp/candidate")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentWorkerIsolation(app.ID, candidate.ID, "grouped"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ServingWorkerIsolation(app, "grouped"); err != nil || got != "multiplex" {
		t.Fatalf("unpublished candidate changed serving mode=%q %v", got, err)
	}
}
