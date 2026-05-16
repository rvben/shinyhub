package lifecycle_test

import (
	"context"
	"io"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

// blockingRuntime is a minimal Runtime whose Wait blocks until the test ends,
// so an Adopt'd entry stays live (and thus protected from the sweep) for the
// duration of the test instead of immediately transitioning to crashed.
type blockingRuntime struct{ done chan struct{} }

func (b *blockingRuntime) Start(context.Context, process.StartParams, io.Writer) (process.RunHandle, error) {
	return process.RunHandle{}, nil
}
func (b *blockingRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (b *blockingRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	select {
	case <-b.done:
	case <-ctx.Done():
	}
	return nil
}
func (b *blockingRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (b *blockingRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (b *blockingRuntime) HostPreparesDeps() bool { return false }
func (b *blockingRuntime) AppBindHost() string    { return "127.0.0.1" }

// fakeSweeper implements lifecycle.ContainerSweeper, recording every container
// it is asked to remove.
type fakeSweeper struct {
	containers []process.ContainerInfo
	removed    []string
	removeErr  error
}

func (f *fakeSweeper) ListByLabel(string) ([]process.ContainerInfo, error) {
	return f.containers, nil
}

func (f *fakeSweeper) RemoveHandle(h process.RunHandle) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, h.ContainerID)
	return nil
}

// TestSweepOrphanContainers verifies the startup sweep removes app-replica
// containers that no live replica re-adopted, while protecting (1) containers
// the Manager re-adopted and (2) one-shot schedule-run containers, which carry
// shinyhub.managed but no replica_index and run with AutoRemove.
func TestSweepOrphanContainers(t *testing.T) {
	rt := &blockingRuntime{done: make(chan struct{})}
	t.Cleanup(func() { close(rt.done) })
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.Adopt("live-app", process.ProcessInfo{
		Slug: "live-app", Index: 0, Status: process.StatusRunning,
	}, process.RunHandle{ContainerID: "c-live"})

	sw := &fakeSweeper{containers: []process.ContainerInfo{
		{ID: "c-live", Labels: map[string]string{
			"shinyhub.managed": "true", "shinyhub.slug": "live-app", "shinyhub.replica_index": "0"}},
		{ID: "c-orphan", Labels: map[string]string{
			"shinyhub.managed": "true", "shinyhub.slug": "deleted-app", "shinyhub.replica_index": "0"}},
		{ID: "c-shrunk", Labels: map[string]string{
			"shinyhub.managed": "true", "shinyhub.slug": "live-app", "shinyhub.replica_index": "3"}},
		{ID: "c-sched", Labels: map[string]string{
			"shinyhub.managed": "true", "shinyhub.slug": "live-app", "shinyhub.kind": "schedule-run"}},
	}}

	lifecycle.SweepOrphanContainers(mgr, sw)

	want := map[string]bool{"c-orphan": true, "c-shrunk": true}
	if len(sw.removed) != len(want) {
		t.Fatalf("removed = %v, want exactly %v", sw.removed, want)
	}
	for _, id := range sw.removed {
		if !want[id] {
			t.Errorf("removed %q, which should have been protected", id)
		}
	}
}

// TestSweepOrphanContainers_NilSweeperNoop verifies native runtime (no
// sweeper) is a safe no-op.
func TestSweepOrphanContainers_NilSweeperNoop(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	lifecycle.SweepOrphanContainers(mgr, nil) // must not panic
}
