package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/fargate"
)

// TestWorkerDeclaredGone_ECSManagedWorkersNeverGone asserts that the synthetic
// ECS worker identities (both Fargate and EC2) are never declared gone,
// preventing ECS inventory blips from permanently stranding replicas.
func TestWorkerDeclaredGone_ECSManagedWorkersNeverGone(t *testing.T) {
	// Passing a nil store is safe because the ECS guard returns false before
	// the store.GetWorker call.
	for _, id := range []string{fargate.WorkerID, fargate.EC2WorkerID} {
		if workerDeclaredGone(nil, id) {
			t.Errorf("workerDeclaredGone(%q) = true, want false (ECS replicas must not be declared gone on blip)", id)
		}
	}
}
