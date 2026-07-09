package proxy

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestElasticWorkersSnapshot verifies the exported capacity view of an
// elastic pool: one entry per worker, sorted by slot, with the live routing
// status and bound-session count the admission path maintains.
func TestElasticWorkersSnapshot(t *testing.T) {
	const slug = "snapapp"

	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, 3, 5)

	// Slot 0: booting with two bound clients.
	s0 := p.reserveWorker(slug, "c1")
	p.bindClient(slug, "c1", s0)
	p.bindClient(slug, "c2", s0)

	// Slot 1: registered (running) with one bound client and deploymentID 42.
	s1 := p.reserveWorker(slug, "c3")
	p.bindClient(slug, "c3", s1)
	if err := p.RegisterElasticWorker(slug, s1, "http://127.0.0.1:1", nil, 42); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}

	// Slot 2: draining, no clients.
	s2 := p.reserveWorker(slug, "c4")
	p.mu.Lock()
	p.pools[slug].workers[s2].status = workerDraining
	p.mu.Unlock()

	snap, ok := p.ElasticWorkersSnapshot(slug)
	if !ok {
		t.Fatal("ElasticWorkersSnapshot returned ok=false for an elastic pool")
	}
	if snap.Mode != string(config.IsolationGrouped) {
		t.Errorf("Mode = %q, want %q", snap.Mode, config.IsolationGrouped)
	}
	if snap.SessionsPerWorker != 3 {
		t.Errorf("SessionsPerWorker = %d, want 3", snap.SessionsPerWorker)
	}
	if snap.MaxWorkers != 5 {
		t.Errorf("MaxWorkers = %d, want 5", snap.MaxWorkers)
	}
	if len(snap.Workers) != 3 {
		t.Fatalf("len(Workers) = %d, want 3", len(snap.Workers))
	}
	for i := 1; i < len(snap.Workers); i++ {
		if snap.Workers[i-1].SlotID >= snap.Workers[i].SlotID {
			t.Fatalf("workers not sorted by slot: %+v", snap.Workers)
		}
	}

	byID := map[int]ElasticWorkerStatus{}
	for _, w := range snap.Workers {
		byID[w.SlotID] = w
	}
	if w := byID[s0]; w.Status != "booting" || w.Sessions != 2 {
		t.Errorf("slot %d = %+v, want status booting sessions 2", s0, w)
	}
	if w := byID[s1]; w.Status != "running" || w.Sessions != 1 || w.DeploymentID != 42 {
		t.Errorf("slot %d = %+v, want status running sessions 1 deploymentID 42", s1, w)
	}
	if w := byID[s2]; w.Status != "draining" || w.Sessions != 0 {
		t.Errorf("slot %d = %+v, want status draining sessions 0", s2, w)
	}
}

// TestElasticWorkersSnapshot_PerSessionCap pins the per-worker cap for
// per_session mode: always 1, regardless of the stored grouped size.
func TestElasticWorkersSnapshot_PerSessionCap(t *testing.T) {
	p := New()
	p.SetPoolMode("ps", config.IsolationPerSession, 0, 4)
	snap, ok := p.ElasticWorkersSnapshot("ps")
	if !ok {
		t.Fatal("want ok=true for a per_session pool")
	}
	if snap.SessionsPerWorker != 1 {
		t.Errorf("SessionsPerWorker = %d, want 1", snap.SessionsPerWorker)
	}
	if len(snap.Workers) != 0 {
		t.Errorf("Workers = %+v, want empty (no workers spawned)", snap.Workers)
	}
}

// TestElasticWorkersSnapshot_NonElastic verifies multiplex and unknown pools
// report ok=false: a missing capacity view must be distinguishable from an
// elastic pool that currently has zero workers.
func TestElasticWorkersSnapshot_NonElastic(t *testing.T) {
	p := New()
	p.SetPoolMode("mux", config.IsolationMultiplex, 0, 0)
	if _, ok := p.ElasticWorkersSnapshot("mux"); ok {
		t.Error("want ok=false for a multiplex pool")
	}
	if _, ok := p.ElasticWorkersSnapshot("missing"); ok {
		t.Error("want ok=false for an unknown slug")
	}
}
