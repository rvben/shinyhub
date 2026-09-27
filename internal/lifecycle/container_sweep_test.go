package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

// blockingRuntime is a minimal Runtime whose Wait blocks until the test ends,
// so an Adopt'd entry stays live (and thus protected from the sweep) for the
// duration of the test instead of immediately transitioning to crashed.
type blockingRuntime struct{ done chan struct{} }

func (b *blockingRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	id := fmt.Sprintf("blocking-%d", p.Port)
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "docker",
		WorkerID: id,
		Handle:   process.RunHandle{ContainerID: id},
	}, nil
}
func (b *blockingRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (b *blockingRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	select {
	case <-b.done:
	case <-ctx.Done():
	}
	return nil
}
func (b *blockingRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (b *blockingRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (b *blockingRuntime) HostPreparesDeps() bool    { return false }
func (b *blockingRuntime) AppBindHost() string       { return "127.0.0.1" }
func (b *blockingRuntime) HostProvidesAppData() bool { return true }

// fakeSweeper implements lifecycle.ContainerSweeper, recording every container
// it is asked to remove.
type fakeSweeper struct {
	containers []process.ContainerInfo
	removed    []string
	removeErr  error
	listErr    error
	listCalls  int
}

func (f *fakeSweeper) ListByLabel(string) ([]process.ContainerInfo, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeSweeper) RemoveHandle(h process.RunHandle) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, h.ContainerID)
	return nil
}

type tierSweepRuntime struct {
	*blockingRuntime
	*fakeSweeper
	scope string
}

func (r *tierSweepRuntime) ContainerSweepScope() string { return r.scope }

// TestSweepOrphanContainers verifies the startup sweep protects re-adopted app
// replicas but removes every other managed container, including an orphaned
// one-shot producer that could otherwise keep writing after owner failover.
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

	if err := lifecycle.SweepOrphanContainers(mgr, sw); err != nil {
		t.Fatalf("a complete sweep reported %v", err)
	}

	want := map[string]bool{"c-orphan": true, "c-shrunk": true, "c-sched": true}
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

func TestFenceOrphanScheduleContainersForTiers_FailsClosed(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	sweeper := &fakeSweeper{containers: []process.ContainerInfo{
		{ID: "replica", Labels: map[string]string{process.LabelManaged: "true", process.LabelReplicaIndex: "0"}},
		{ID: "producer", Labels: map[string]string{process.LabelManaged: "true", process.LabelKind: process.KindScheduleRun}},
	}}
	runtime := &tierSweepRuntime{blockingRuntime: &blockingRuntime{done: make(chan struct{})}, fakeSweeper: sweeper, scope: "daemon"}
	t.Cleanup(func() { close(runtime.done) })
	mgr.RegisterRuntime("docker", runtime)

	if err := lifecycle.FenceOrphanScheduleContainersForTiers(mgr, []string{"docker"}); err != nil {
		t.Fatalf("fence orphan producers: %v", err)
	}
	if len(sweeper.removed) != 1 || sweeper.removed[0] != "producer" {
		t.Fatalf("removed = %v, want only producer", sweeper.removed)
	}

	sweeper.removeErr = fmt.Errorf("daemon unavailable")
	if err := lifecycle.FenceOrphanScheduleContainersForTiers(mgr, []string{"docker"}); err == nil {
		t.Fatal("removal uncertainty must fail the startup fence")
	}
}

func TestSweepOrphanContainersForTiers_IncludesNonDefaultAndDeduplicatesDaemon(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	sweeper := &fakeSweeper{containers: []process.ContainerInfo{{
		ID: "burst-orphan", Labels: map[string]string{
			process.LabelManaged: "true", process.LabelSlug: "mixed", process.LabelReplicaIndex: "1",
		},
	}}}
	runtime := &tierSweepRuntime{
		blockingRuntime: &blockingRuntime{done: make(chan struct{})},
		fakeSweeper:     sweeper,
		scope:           "unix:///var/run/docker.sock",
	}
	t.Cleanup(func() { close(runtime.done) })
	mgr.RegisterRuntime("burst", runtime)
	mgr.RegisterRuntime("burst-alias", runtime)

	lifecycle.SweepOrphanContainersForTiers(mgr, []string{"local", "burst", "burst-alias"})

	if sweeper.listCalls != 1 {
		t.Fatalf("container inventory calls=%d, want 1 for shared daemon", sweeper.listCalls)
	}
	if len(sweeper.removed) != 1 || sweeper.removed[0] != "burst-orphan" {
		t.Fatalf("removed=%v, want [burst-orphan]", sweeper.removed)
	}
}

// A sweep that leaves an orphan possibly running says so, because the
// startup result cache prune must not run under a container it did not see.
func TestSweepOrphanContainers_ReportsWhatItCouldNotRemove(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	orphan := process.ContainerInfo{ID: "c-orphan", Labels: map[string]string{
		process.LabelManaged: "true", process.LabelSlug: "gone", process.LabelReplicaIndex: "0"}}

	listFail := &fakeSweeper{listErr: errors.New("daemon unreachable")}
	if err := lifecycle.SweepOrphanContainers(mgr, listFail); err == nil {
		t.Error("a failed listing was reported as a complete sweep")
	}
	removeFail := &fakeSweeper{containers: []process.ContainerInfo{orphan}, removeErr: errors.New("busy")}
	if err := lifecycle.SweepOrphanContainers(mgr, removeFail); err == nil || !strings.Contains(err.Error(), "c-orphan") {
		t.Errorf("err = %v, want one naming the orphan that survived", err)
	}
	if err := lifecycle.SweepOrphanContainers(mgr, nil); err != nil {
		t.Errorf("the native no-op reported %v", err)
	}
}

func TestSweepOrphanContainersForTiers_JoinsTierFailures(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	bad := &tierSweepRuntime{blockingRuntime: &blockingRuntime{done: make(chan struct{})},
		fakeSweeper: &fakeSweeper{listErr: errors.New("daemon unreachable")}, scope: "bad"}
	good := &tierSweepRuntime{blockingRuntime: &blockingRuntime{done: make(chan struct{})},
		fakeSweeper: &fakeSweeper{}, scope: "good"}
	t.Cleanup(func() { close(bad.done); close(good.done) })
	mgr.RegisterRuntime("bad", bad)
	mgr.RegisterRuntime("good", good)

	if err := lifecycle.SweepOrphanContainersForTiers(mgr, []string{"good"}); err != nil {
		t.Fatalf("a clean tier reported %v", err)
	}
	err := lifecycle.SweepOrphanContainersForTiers(mgr, []string{"good", "bad"})
	if err == nil || !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("err = %v, want one naming the failed tier", err)
	}
	if good.listCalls != 2 {
		t.Errorf("good tier listed %d times, want 2: one failing tier must not skip the others", good.listCalls)
	}
}
