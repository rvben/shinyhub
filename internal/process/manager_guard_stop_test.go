package process_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

type closingStartupGuard struct {
	closed chan struct{}
	once   sync.Once
}

func (*closingStartupGuard) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (g *closingStartupGuard) Close() error {
	g.once.Do(func() { close(g.closed) })
	return nil
}

type guardStopRaceRuntime struct {
	*failOnceThenConfirmRuntime
	guard        closingStartupGuard
	signalFailed chan struct{}
	signalOnce   sync.Once
}

func (r *guardStopRaceRuntime) Start(ctx context.Context, p process.StartParams, log io.Writer) (process.ReplicaEndpoint, error) {
	ep, err := r.failOnceThenConfirmRuntime.Start(ctx, p, log)
	ep.StartupGuard = &r.guard
	return ep, err
}

func (r *guardStopRaceRuntime) Signal(process.RunHandle, syscall.Signal) error {
	<-r.guard.closed
	r.signalOnce.Do(func() { close(r.signalFailed) })
	return syscall.EPERM
}

func TestManager_GuardCloseWaitsForExitAfterFailedSignal(t *testing.T) {
	rt := &guardStopRaceRuntime{
		failOnceThenConfirmRuntime: newFailOnceThenConfirmRuntime(),
		guard:                      closingStartupGuard{closed: make(chan struct{})}, signalFailed: make(chan struct{}),
	}
	t.Cleanup(func() { rt.once.Do(func() { close(rt.exited) }) })
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(time.Second)
	if _, err := m.Start(process.StartParams{Slug: "guard-exit", Command: []string{"x"}, Port: 1}); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- m.StopReplicaConfirmed("guard-exit", 0) }()
	<-rt.signalFailed
	// The guard is closed and the signal failed, but the exit monitor has not
	// reaped the supervisor yet. Shutdown must wait for that independent proof.
	select {
	case err := <-stopped:
		t.Fatalf("stop returned before guard exit was observed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	rt.once.Do(func() { close(rt.exited) })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("confirmed guard exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not observe guard exit")
	}
	if _, _, ok := m.ReplicaIncarnation("guard-exit", 0); ok {
		t.Fatal("confirmed guard exit left the slot occupied")
	}
}

func TestManager_GuardCloseDoesNotHideSignalFailureWithoutExit(t *testing.T) {
	rt := &guardStopRaceRuntime{
		failOnceThenConfirmRuntime: newFailOnceThenConfirmRuntime(),
		guard:                      closingStartupGuard{closed: make(chan struct{})}, signalFailed: make(chan struct{}),
	}
	t.Cleanup(func() { rt.once.Do(func() { close(rt.exited) }) })
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)
	if _, err := m.Start(process.StartParams{Slug: "guard-live", Command: []string{"x"}, Port: 1}); err != nil {
		t.Fatal(err)
	}
	if err := m.StopReplicaConfirmed("guard-live", 0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("stop without exit proof = %v, want permission error", err)
	}
	if _, _, ok := m.ReplicaIncarnation("guard-live", 0); !ok {
		t.Fatal("unconfirmed guard exit was forgotten")
	}
}
