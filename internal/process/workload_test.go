package process

import (
	"context"
	"io"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestNativeWorkloadObservationLifecycle(t *testing.T) {
	rt := NewNativeRuntime()
	var starts, ends atomic.Int32
	rt.SetWorkloadObserver(func(p StartParams, handle RunHandle, cgroup string) func() {
		// Callback reentry is safe: no internal runtime lock may be held.
		rt.mu.Lock()
		rt.mu.Unlock()
		if handle.PID <= 0 || p.Slug != "app" {
			t.Errorf("wrong launch identity")
		}
		starts.Add(1)
		return func() { ends.Add(1) }
	})
	p := StartParams{Slug: "app", Command: []string{"/bin/sh", "-c", "sleep 10"}, Dir: t.TempDir()}
	ep, err := rt.Start(context.Background(), p, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Signal(ep.Handle, syscall.SIGKILL) }()
	rt.ObserveWorkload(p, ep.Handle) // repeated recovery registration is harmless
	if starts.Load() != 1 {
		t.Fatalf("starts = %d", starts.Load())
	}
	if err := rt.Signal(ep.Handle, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = rt.Wait(context.Background(), ep.Handle)
	if ends.Load() != 1 || len(rt.observationEnds) != 0 {
		t.Fatalf("ends = %d, registrations = %d", ends.Load(), len(rt.observationEnds))
	}
	_, err = rt.RunOnce(context.Background(), StartParams{Slug: "app", Dir: t.TempDir(), Command: []string{"/no-such-command"}}, io.Discard)
	if err == nil || starts.Load() != 1 {
		t.Fatal("failed launch emitted observation")
	}
}

func TestObservationExitCanRaceRegistration(t *testing.T) {
	rt := NewNativeRuntime()
	var ends atomic.Int32
	rt.SetWorkloadObserver(func(_ StartParams, handle RunHandle, _ string) func() {
		rt.finishObservation(handle.PID)
		return func() { ends.Add(1) }
	})
	rt.ObserveWorkload(StartParams{}, RunHandle{PID: 10})
	if ends.Load() != 1 || len(rt.observationEnds) != 0 {
		t.Fatal("exit during registration leaked an observation")
	}
}
