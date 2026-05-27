package proxy_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestDeregisterReplicaIfTarget asserts the slot is removed only while its
// current target still matches the expected URL, so a worker-loss pass cannot
// pull a route that a concurrent redeploy already re-pointed at a healthy
// backend.
func TestDeregisterReplicaIfTarget(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	if err := p.RegisterReplica("demo", 0, "http://10.0.0.1:9/v1/data/tok", nil); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Stale expectation (route was re-pointed): no-op.
	if removed := p.DeregisterReplicaIfTarget("demo", 0, "http://10.0.0.2:9/v1/data/old"); removed {
		t.Fatal("deregistered a slot whose target did not match the expected URL")
	}
	if got := p.ReplicaTargetURL("demo", 0); got != "http://10.0.0.1:9/v1/data/tok" {
		t.Fatalf("route wrongly disturbed: %q", got)
	}

	// Matching expectation: removed.
	if removed := p.DeregisterReplicaIfTarget("demo", 0, "http://10.0.0.1:9/v1/data/tok"); !removed {
		t.Fatal("matching target was not deregistered")
	}
	if got := p.ReplicaTargetURL("demo", 0); got != "" {
		t.Fatalf("slot still routable after matching deregister: %q", got)
	}

	// Unknown pool / out-of-range index / empty slot: safe no-ops.
	if p.DeregisterReplicaIfTarget("ghost", 0, "x") {
		t.Error("unknown pool reported a removal")
	}
	if p.DeregisterReplicaIfTarget("demo", 9, "x") {
		t.Error("out-of-range index reported a removal")
	}
	if p.DeregisterReplicaIfTarget("demo", 0, "x") {
		t.Error("empty slot reported a removal")
	}
}
