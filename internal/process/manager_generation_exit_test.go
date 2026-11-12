package process_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// A candidate generation lives in its own pool beside the active one, at the
// same replica index. When the candidate's process exits, its exit must be
// recorded on the candidate and must leave the active replica at that index
// alone.
func TestManager_GenerationReplicaExitIsRecordedOnItsOwnPool(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	defer mgr.StopAll()

	active, err := mgr.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := mgr.Start(process.StartParams{
		Slug: "demo", Index: 0, Port: 20002, Command: []string{"app"}, DeploymentID: 202, GenerationScoped: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.PID == active.PID {
		t.Fatalf("candidate and active replica share PID %d; the test cannot tell them apart", active.PID)
	}

	rt.exit(candidate.PID)

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, ok := mgr.GetGenerationReplica("demo", 202, 0)
		if !ok {
			t.Fatal("candidate replica disappeared from its generation pool")
		}
		if got.Status == process.StatusCrashed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("candidate status = %q after its process exited, want %q", got.Status, process.StatusCrashed)
		}
		time.Sleep(5 * time.Millisecond)
	}

	still, ok := mgr.GetReplica("demo", 0)
	if !ok {
		t.Fatal("active replica is gone after the candidate exited")
	}
	if still.PID != active.PID || still.Status != process.StatusRunning {
		t.Fatalf("active replica = pid %d status %q, want pid %d still %q", still.PID, still.Status, active.PID, process.StatusRunning)
	}
}
