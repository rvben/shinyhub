package process_test

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// safeBuffer is a bytes.Buffer protected by a mutex so the test goroutine and
// the os/exec stdout-copy goroutine can share it without racing. Plain
// bytes.Buffer is unsafe for concurrent Write+Read.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func TestNativeRuntimeStartStop(t *testing.T) {
	rt := process.NewNativeRuntime()
	var buf bytes.Buffer

	handle, err := rt.Start(context.Background(), process.StartParams{
		Slug:    "test",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19100,
	}, &buf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.PID <= 0 {
		t.Fatalf("expected valid PID, got %d", handle.PID)
	}

	if err := rt.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- rt.Wait(context.Background(), handle) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait timed out")
	}

	if err := syscall.Kill(handle.PID, 0); err == nil {
		t.Error("expected process to be dead after Signal+Wait")
	}
}

// TestNativeRuntimeStart_InjectsAppDataEnv guards the long-running-process
// path: when the Manager has stamped p.AppDataPath onto StartParams, the
// runtime must put SHINYHUB_APP_DATA in the child env so apps can locate
// their persistent data dir. Symmetric to RunOnce; both call sites share the
// contract via nativeChildEnv.
func TestNativeRuntimeStart_InjectsAppDataEnv(t *testing.T) {
	rt := process.NewNativeRuntime()
	appData := t.TempDir()
	var buf safeBuffer

	handle, err := rt.Start(context.Background(), process.StartParams{
		Slug: "envprobe", Dir: t.TempDir(),
		// Print env to stdout, then exec a long sleep so the test controls
		// teardown via Signal+Wait.
		Command:     []string{"sh", "-c", "printf %s \"$SHINYHUB_APP_DATA\"; exec sleep 30"},
		AppDataPath: appData,
	}, &buf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Signal(handle, syscall.SIGTERM)
		_ = rt.Wait(context.Background(), handle)
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := buf.String(); got != appData {
		t.Errorf("SHINYHUB_APP_DATA in child = %q, want %q", got, appData)
	}
}

// TestNativeRuntimeStart_PlatformOverridesUserAppDataEnv verifies that a
// user-supplied SHINYHUB_APP_DATA in StartParams.Env cannot shadow the
// platform value derived from StartParams.AppDataPath on the long-running
// path. Sibling of the RunOnce variant.
func TestNativeRuntimeStart_PlatformOverridesUserAppDataEnv(t *testing.T) {
	rt := process.NewNativeRuntime()
	appData := t.TempDir()
	var buf safeBuffer

	handle, err := rt.Start(context.Background(), process.StartParams{
		Slug: "envprobe2", Dir: t.TempDir(),
		Command:     []string{"sh", "-c", "printf %s \"$SHINYHUB_APP_DATA\"; exec sleep 30"},
		Env:         []string{"SHINYHUB_APP_DATA=/evil"},
		AppDataPath: appData,
	}, &buf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Signal(handle, syscall.SIGTERM)
		_ = rt.Wait(context.Background(), handle)
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := buf.String(); got != appData {
		t.Errorf("SHINYHUB_APP_DATA = %q, want %q (platform must win over user env)", got, appData)
	}
}

func TestNativeRuntimeEmptyCommand(t *testing.T) {
	rt := process.NewNativeRuntime()
	_, err := rt.Start(context.Background(), process.StartParams{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// TestNativeRuntimeWaitBlocksForAdoptedPID guards against the regression where
// Wait returned nil immediately for any PID it didn't start itself. After a
// server restart, recovery adopts surviving children by PID alone — if Wait
// returns immediately, the Manager's monitor goroutine flips them to
// StatusCrashed and the watchdog tries to restart already-running processes.
func TestNativeRuntimeWaitBlocksForAdoptedPID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	rt := process.NewNativeRuntime()
	handle := process.RunHandle{PID: pid}

	waitDone := make(chan error, 1)
	go func() { waitDone <- rt.Wait(context.Background(), handle) }()

	// Wait must not return while the adopted process is still alive.
	select {
	case err := <-waitDone:
		t.Fatalf("Wait returned prematurely for live adopted PID: err=%v", err)
	case <-time.After(500 * time.Millisecond):
		// Good: still blocking.
	}

	// Kill the process and reap it so the kernel reports the PID gone.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("reap: %v", err)
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error after process exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after adopted process exited")
	}
}

// TestNativeRuntimeWaitRespectsContextForAdoptedPID ensures a cancellable
// context is honoured during PID polling, so callers can shut the goroutine
// down on server stop instead of leaking it forever.
func TestNativeRuntimeWaitRespectsContextForAdoptedPID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	rt := process.NewNativeRuntime()
	handle := process.RunHandle{PID: cmd.Process.Pid}

	ctx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() { waitDone <- rt.Wait(ctx, handle) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-waitDone:
		if err == nil {
			t.Fatal("Wait should return ctx.Err() when cancelled with live PID")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not honour ctx cancellation for adopted PID")
	}
}
