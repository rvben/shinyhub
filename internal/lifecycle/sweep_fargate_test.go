package lifecycle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

// noopRuntime is a stub Runtime whose Wait returns immediately, so Adopt's
// monitoring goroutine does not poll real PIDs or dereference a nil receiver.
type noopRuntime struct{}

func (noopRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, nil
}
func (noopRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (noopRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (noopRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (noopRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (noopRuntime) HostPreparesDeps() bool                             { return false }
func (noopRuntime) AppBindHost() string                                { return "127.0.0.1" }
func (noopRuntime) HostProvidesAppData() bool                         { return false }
func (noopRuntime) ReplicaTransportForWorker(_ string) http.RoundTripper { return nil }

// fakeFargateTaskSweeper implements lifecycle.FargateTaskSweeper for testing.
type fakeFargateTaskSweeper struct {
	tasks   []process.TaskRef
	stopped []string
	listErr error
	stopErr error
}

func (f *fakeFargateTaskSweeper) ListManagedTasks(_ context.Context) ([]process.TaskRef, error) {
	return f.tasks, f.listErr
}

func (f *fakeFargateTaskSweeper) StopTask(_ context.Context, arn string) error {
	f.stopped = append(f.stopped, arn)
	return f.stopErr
}

func TestSweepOrphanFargateTasks_StopsUnownedTask(t *testing.T) {
	// Two tasks: one owned by a live replica, one orphaned.
	// Only the orphan should be stopped.
	mgr := process.NewManager(".", noopRuntime{})
	mgr.Adopt("myapp", process.ProcessInfo{
		Slug:   "myapp",
		Index:  0,
		Status: process.StatusRunning,
	}, process.RunHandle{ContainerID: fargate.WorkerID + "/arn-owned"})

	sweeper := &fakeFargateTaskSweeper{
		tasks: []process.TaskRef{
			{ARN: "arn-owned"},
			{ARN: "arn-orphan"},
		},
	}
	lifecycle.SweepOrphanFargateTasks(context.Background(), mgr, sweeper)

	if len(sweeper.stopped) != 1 || sweeper.stopped[0] != "arn-orphan" {
		t.Errorf("stopped = %v, want [arn-orphan]", sweeper.stopped)
	}
}

func TestSweepOrphanFargateTasks_NilSweeperIsNoOp(t *testing.T) {
	mgr := process.NewManager(".", noopRuntime{})
	// Must not panic.
	lifecycle.SweepOrphanFargateTasks(context.Background(), mgr, nil)
}

func TestSweepOrphanFargateTasks_ListErrorSkipsSweep(t *testing.T) {
	mgr := process.NewManager(".", noopRuntime{})
	sweeper := &fakeFargateTaskSweeper{
		listErr: fmt.Errorf("AWS error"),
	}
	// Must not panic; no stops attempted.
	lifecycle.SweepOrphanFargateTasks(context.Background(), mgr, sweeper)
	if len(sweeper.stopped) != 0 {
		t.Errorf("stopped = %v, want empty after list error", sweeper.stopped)
	}
}
