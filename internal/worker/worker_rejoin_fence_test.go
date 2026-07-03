// internal/worker/worker_rejoin_fence_test.go
package worker

import "testing"

// TestWorkerRejoinFence_EndToEnd drives the real store + Registry through the
// full fencing sequence a network partition triggers: the down-monitor reaps a
// worker (bumping its incarnation and reassigning its replicas elsewhere), the
// partition heals, and the worker rejoins still reporting its stale
// incarnation. It must be fenced (kept down, not re-upped for placement) even
// though its tier slot looks free; only after it adopts the current
// incarnation does it re-up cleanly. This is the end-to-end integration
// coverage for reassign -> stale rejoin -> fenced -> clean re-up (Tasks 1-8
// each cover one leg of this in isolation: reap bumps incarnation, heartbeat
// fences a stale worker, the agent self-fences).
func TestWorkerRejoinFence_EndToEnd(t *testing.T) {
	reg, _ := newTestRegistry(t)
	w1, err := reg.Register(RegisterParams{Tier: "burst", AdvertiseAddr: "203.0.113.2:9000"})
	if err != nil {
		t.Fatalf("register w1: %v", err)
	}
	if _, _, err := reg.Heartbeat(w1.NodeID, "fp1", w1.Incarnation); err != nil {
		t.Fatalf("first heartbeat w1: %v", err)
	}
	if got, ok := reg.Worker(w1.NodeID); !ok || got.Status != "up" {
		t.Fatalf("w1 status after first heartbeat = %+v ok=%v, want up", got, ok)
	}

	// Reap w1 (partition detected). This is what the down-monitor does: bump
	// the incarnation and mark it down so its replicas can be reassigned.
	if err := reg.Reap(w1.NodeID); err != nil {
		t.Fatalf("reap w1: %v", err)
	}

	// A different worker takes over (reassignment target); irrelevant to the
	// fence assertion but models the real topology.
	w2, err := reg.Register(RegisterParams{Tier: "burst", AdvertiseAddr: "203.0.113.3:9000"})
	if err != nil {
		t.Fatalf("register w2: %v", err)
	}
	if _, _, err := reg.Heartbeat(w2.NodeID, "fp2", w2.Incarnation); err != nil {
		t.Fatalf("heartbeat w2: %v", err)
	}

	// w1's partition heals; it heartbeats still reporting its stale
	// incarnation (w1.Incarnation == 1, stored incarnation is now 2 after Reap).
	fenced, current, err := reg.Heartbeat(w1.NodeID, "fp1", w1.Incarnation)
	if err != nil {
		t.Fatalf("rejoin heartbeat w1: %v", err)
	}
	if !fenced {
		t.Fatal("rejoining reaped worker must be fenced")
	}
	if got, _ := reg.Worker(w1.NodeID); got.Status == "up" {
		t.Fatal("fenced worker must not be re-upped (would get new placements)")
	}

	// w1 adopts the current incarnation and heartbeats again -> re-ups clean.
	fenced2, _, err := reg.Heartbeat(w1.NodeID, "fp1", current)
	if err != nil {
		t.Fatalf("re-up heartbeat w1: %v", err)
	}
	if fenced2 {
		t.Fatal("worker at the current incarnation must not be fenced")
	}
	if got, _ := reg.Worker(w1.NodeID); got.Status != "up" {
		t.Fatalf("worker should re-up after adopting incarnation, got %s", got.Status)
	}
}
