package lifecycle

import (
	"context"
	"io"
	"net/http"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// stubRuntime is a Runtime whose Wait returns immediately so Adopt's monitoring
// goroutine does not hang waiting for a real process or a PID 0 kernel poll.
type stubRuntime struct{}

func (stubRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, nil
}
func (stubRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (stubRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (stubRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (stubRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (stubRuntime) HostPreparesDeps() bool                             { return false }
func (stubRuntime) AppBindHost() string                                { return "127.0.0.1" }
func (stubRuntime) HostProvidesAppData() bool                         { return false }
func (stubRuntime) ReplicaTransportForWorker(_ string) http.RoundTripper { return nil }

func buildTestApp(slug string, replicas int) *db.App {
	return &db.App{
		ID:       1,
		Slug:     slug,
		Status:   "running",
		Replicas: replicas,
	}
}

func buildTestReplica(appID int64, index int, workerID string, deploymentID *int64) *db.Replica {
	return &db.Replica{
		AppID:        appID,
		Index:        index,
		Status:       db.ReplicaStatusRunning,
		WorkerID:     workerID,
		DeploymentID: deploymentID,
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestRecoverRemoteReplica_FargatePartialAdoptWhenNoURL(t *testing.T) {
	// A Fargate task is PROVISIONING: Running=true, URL="".
	// recoverRemoteReplica should do a partial adopt (claim slot) and return true.
	store := openMemStore(t)
	mgr := process.NewManager(".", stubRuntime{})
	prx := proxy.New()

	app := buildTestApp("demo", 1)
	depID := int64(42)
	r := buildTestReplica(app.ID, 0, fargate.WorkerID, &depID)

	items := []process.InventoryItem{{
		ContainerID: "arn-pending",
		Labels: map[string]string{
			"shinyhub.slug":          "demo",
			"shinyhub.replica_index": "0",
			"shinyhub.deployment_id": "42",
		},
		Running:  true,  // PROVISIONING = not stopped
		URL:      "",    // no IP yet
		WorkerID: fargate.WorkerID,
	}}

	adopted := recoverRemoteReplica(store, mgr, prx, app, r, items)
	if !adopted {
		t.Fatal("recoverRemoteReplica returned false for a partial-adopt; want true so anyAlive is set")
	}
	// The replica must be in the manager (slot claimed).
	info, ok := mgr.GetReplica("demo", 0)
	if !ok {
		t.Fatal("replica not in manager after partial adopt")
	}
	if info.WorkerID != fargate.WorkerID {
		t.Errorf("WorkerID = %q, want %q", info.WorkerID, fargate.WorkerID)
	}
}

func TestRecoverRemoteReplica_FargateFullAdoptWhenURLPresent(t *testing.T) {
	// A Fargate task is fully RUNNING with an IP.
	// recoverRemoteReplica should do a full adopt with proxy registration and return true.
	store := openMemStore(t)
	mgr := process.NewManager(".", stubRuntime{})
	prx := proxy.New()
	prx.SetPoolSize("demo", 1)

	app := buildTestApp("demo", 1)
	depID := int64(42)
	r := buildTestReplica(app.ID, 0, fargate.WorkerID, &depID)

	items := []process.InventoryItem{{
		ContainerID: "arn-running",
		Labels: map[string]string{
			"shinyhub.slug":          "demo",
			"shinyhub.replica_index": "0",
			"shinyhub.deployment_id": "42",
		},
		Running:  true,
		URL:      "http://192.0.2.5:8000",
		WorkerID: fargate.WorkerID,
	}}

	adopted := recoverRemoteReplica(store, mgr, prx, app, r, items)
	if !adopted {
		t.Fatal("recoverRemoteReplica returned false for a full-adopt; want true")
	}
	info, ok := mgr.GetReplica("demo", 0)
	if !ok {
		t.Fatal("replica not in manager after full adopt")
	}
	if info.EndpointURL != "http://192.0.2.5:8000" {
		t.Errorf("EndpointURL = %q, want http://192.0.2.5:8000", info.EndpointURL)
	}
}

func TestRecoverRemoteReplica_RemoteDockerEmptyURLReturnsFalse(t *testing.T) {
	// A remote_docker replica with empty URL is a genuine error (worker did not
	// report a URL for a RUNNING container). Must NOT be partially adopted.
	store := openMemStore(t)
	mgr := process.NewManager(".", stubRuntime{})
	prx := proxy.New()

	app := buildTestApp("demo", 1)
	depID := int64(10)
	r := buildTestReplica(app.ID, 0, "worker-node-1", &depID) // not fargate

	items := []process.InventoryItem{{
		ContainerID: "container-abc",
		Labels: map[string]string{
			"shinyhub.slug":          "demo",
			"shinyhub.replica_index": "0",
			"shinyhub.deployment_id": "10",
		},
		Running:  true,
		URL:      "",             // empty URL for remote_docker = error
		WorkerID: "worker-node-1",
	}}

	adopted := recoverRemoteReplica(store, mgr, prx, app, r, items)
	if adopted {
		t.Fatal("recoverRemoteReplica returned true for remote_docker with empty URL; regression - must return false")
	}
	if _, ok := mgr.GetReplica("demo", 0); ok {
		t.Fatal("replica must not be in manager after rejected remote_docker adoption")
	}
}

func TestRecoverRemoteReplica_StoppedTaskReturnsFalse(t *testing.T) {
	// A STOPPED task (Running=false) must never be adopted.
	store := openMemStore(t)
	mgr := process.NewManager(".", stubRuntime{})
	prx := proxy.New()

	app := buildTestApp("demo", 1)
	depID := int64(42)
	r := buildTestReplica(app.ID, 0, fargate.WorkerID, &depID)

	items := []process.InventoryItem{{
		ContainerID: "arn-stopped",
		Labels: map[string]string{
			"shinyhub.slug":          "demo",
			"shinyhub.replica_index": "0",
			"shinyhub.deployment_id": "42",
		},
		Running:  false, // STOPPED
		URL:      "",
		WorkerID: fargate.WorkerID,
	}}

	adopted := recoverRemoteReplica(store, mgr, prx, app, r, items)
	if adopted {
		t.Fatal("recoverRemoteReplica returned true for STOPPED Fargate task; want false")
	}
}

func TestRecoverRemoteReplica_NilItemReturnsFalse(t *testing.T) {
	store := openMemStore(t)
	mgr := process.NewManager(".", stubRuntime{})
	prx := proxy.New()
	app := buildTestApp("demo", 1)
	depID := int64(1)
	r := buildTestReplica(app.ID, 0, fargate.WorkerID, &depID)

	// Pass empty inventory - no matching item, matchInventoryItem returns nil.
	adopted := recoverRemoteReplica(store, mgr, prx, app, r, nil)
	if adopted {
		t.Fatal("recoverRemoteReplica returned true for nil inventory item; want false")
	}
}
