package process_test

import (
	"github.com/rvben/shinyhub/internal/process"
	"testing"
)

func TestPoolSelectionRestoresEmptyPredecessorAndRejectsStaleCAS(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	previous := mgr.CapturePoolSelection("demo")
	_, err := mgr.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"app"}, DeploymentID: 202, GenerationScoped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.StopAll()
	if _, err := mgr.ActivateGeneration("demo", 202); err != nil {
		t.Fatal(err)
	}
	candidate := mgr.CapturePoolSelection("demo")
	if err := mgr.RestorePoolSelection("demo", candidate, previous); err != nil {
		t.Fatal(err)
	}
	if mgr.CapturePoolSelection("demo") != previous {
		t.Fatal("empty predecessor selection was lost")
	}
	if err := mgr.RestorePoolSelection("demo", candidate, previous); err == nil {
		t.Fatal("stale CAS accepted")
	}
}

func TestReplicaFloorIncludesEveryGeneration(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), newPerPIDRuntime())
	mgr.ForceEntry("demo", process.ProcessInfo{Slug: "demo", Index: 7, DeploymentID: 101, Status: process.StatusStopped})
	if got := mgr.NextReplicaIndex("demo"); got != 8 {
		t.Fatalf("floor=%d, want 8", got)
	}
	if got := len(mgr.AllGenerationsForSlug("demo")); got != 1 {
		t.Fatalf("entries=%d", got)
	}
}
