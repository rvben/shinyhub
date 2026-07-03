package worker

import "testing"

// TestReplicaServer_StopAll_ClearsTrackedReplicas pins the fencing contract:
// StopAll must drop every tracked replica from both byContainer and byToken so
// /v1/data/{token} stops serving them, regardless of whether signalling the
// underlying runtime succeeds.
func TestReplicaServer_StopAll_ClearsTrackedReplicas(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRuntime{}
	s := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 49001 },
	})

	// Seed two records directly into the maps, as handleStart would.
	s.mu.Lock()
	s.byContainer["c1"] = &replicaRecord{token: "t1", containerID: "c1"}
	s.byToken["t1"] = s.byContainer["c1"]
	s.byContainer["c2"] = &replicaRecord{token: "t2", containerID: "c2"}
	s.byToken["t2"] = s.byContainer["c2"]
	s.mu.Unlock()

	s.StopAll()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byContainer) != 0 || len(s.byToken) != 0 {
		t.Fatalf("StopAll must clear tracking maps: byContainer=%d byToken=%d", len(s.byContainer), len(s.byToken))
	}
}
