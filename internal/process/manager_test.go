package process_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeRuntime is an in-process Runtime for tests. It captures the env passed
// to Start and assigns incrementing synthetic PIDs. Wait blocks on a
// per-PID channel that Signal closes when it receives SIGTERM or SIGKILL,
// so Stop()-based test flows terminate cleanly.
type fakeRuntime struct {
	mu      sync.Mutex
	nextPID int
	stops   map[int]chan struct{}
	lastEnv []string
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		nextPID: 10000,
		stops:   make(map[int]chan struct{}),
	}
}

func (f *fakeRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.RunHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastEnv = p.Env
	pid := f.nextPID
	f.nextPID++
	f.stops[pid] = make(chan struct{})
	return process.RunHandle{PID: pid}, nil
}

func (f *fakeRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (f *fakeRuntime) Wait(_ context.Context, h process.RunHandle) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (f *fakeRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

func (f *fakeRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

func (f *fakeRuntime) HostPreparesDeps() bool { return true }

func (f *fakeRuntime) AppBindHost() string { return "127.0.0.1" }

func TestManagerStartStop(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	info, err := m.Start(process.StartParams{
		Slug:    "test-app",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if info.PID <= 0 {
		t.Errorf("expected valid PID, got %d", info.PID)
	}

	time.Sleep(100 * time.Millisecond)

	if err := m.Stop("test-app"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// verify the process is actually gone
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Error("expected process to be dead after Stop")
	}
}

// TestNativeRuntime_AppBindHost asserts the loopback contract for the native
// runtime: app processes share the host network and must be reachable only via
// the in-process proxy.
func TestNativeRuntime_AppBindHost(t *testing.T) {
	if got := process.NewNativeRuntime().AppBindHost(); got != "127.0.0.1" {
		t.Errorf("NativeRuntime.AppBindHost = %q, want 127.0.0.1", got)
	}
}

// TestManager_AppBindHost_ProxiesRuntime locks the contract that Manager
// surfaces the runtime's bind host to deploy-time command builders.
func TestManager_AppBindHost_ProxiesRuntime(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	if got := m.AppBindHost(); got != "127.0.0.1" {
		t.Errorf("Manager.AppBindHost (native) = %q, want 127.0.0.1", got)
	}
}

func TestManagerStatus(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	_, err := m.Start(process.StartParams{
		Slug:    "status-app",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19001,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("status-app")

	info, err := m.Status("status-app")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != process.StatusRunning {
		t.Errorf("expected running, got %s", info.Status)
	}
}

func TestManagerStatusUnknown(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	info, err := m.Status("no-such-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != process.StatusStopped {
		t.Errorf("expected stopped, got %s", info.Status)
	}
	if info.Slug != "no-such-app" {
		t.Errorf("expected slug preserved, got %s", info.Slug)
	}
}

func TestManagerCrashDetection(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	_, err := m.Start(process.StartParams{
		Slug:    "crash-test",
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "exit 1"},
		Port:    19200,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the exit-monitoring goroutine to update status.
	// Status() only returns running replicas; use GetReplica to observe crashed state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, ok := m.GetReplica("crash-test", 0)
		if ok && info.Status == process.StatusCrashed {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("expected StatusCrashed after process exited unexpectedly")
}

func TestManagerAdopt(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	pid := os.Getpid()
	handle := process.RunHandle{PID: pid}

	info := process.ProcessInfo{
		Slug:   "adopted",
		PID:    pid,
		Port:   19201,
		Status: process.StatusRunning,
	}
	m.Adopt("adopted", info, handle)

	got, err := m.Status("adopted")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != process.StatusRunning {
		t.Errorf("expected StatusRunning, got %s", got.Status)
	}
	if got.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, got.PID)
	}

	gotHandle, ok := m.HandleReplica("adopted", 0)
	if !ok {
		t.Fatal("HandleReplica: not found")
	}
	if gotHandle != handle {
		t.Errorf("expected handle %+v, got %+v", handle, gotHandle)
	}
}

func TestManager_PoolStartStop(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)

	p0, err := m.Start(process.StartParams{
		Slug: "demo", Index: 0, Port: 20001, Command: []string{"/bin/true"},
	})
	if err != nil {
		t.Fatalf("start 0: %v", err)
	}
	p1, err := m.Start(process.StartParams{
		Slug: "demo", Index: 1, Port: 20002, Command: []string{"/bin/true"},
	})
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}

	if p0.Index != 0 || p1.Index != 1 {
		t.Fatalf("indices: %d,%d", p0.Index, p1.Index)
	}

	all := m.All()
	if len(all) != 2 {
		t.Fatalf("want 2 entries, got %d", len(all))
	}

	if err := m.StopReplica("demo", 0); err != nil {
		t.Fatalf("stop 0: %v", err)
	}
	all = m.All()
	if len(all) != 1 || all[0].Index != 1 {
		t.Fatalf("after stop: %+v", all)
	}

	if err := m.Stop("demo"); err != nil {
		t.Fatalf("stop pool: %v", err)
	}
	if len(m.All()) != 0 {
		t.Fatalf("pool not empty after Stop")
	}
}

func TestManager_DuplicateIndex(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	_, _ = m.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"/bin/true"}})
	_, err := m.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20002, Command: []string{"/bin/true"}})
	if err == nil {
		t.Fatalf("expected error for duplicate index")
	}
}

func TestStart_PlatformEnvWinsOverUserEnv(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, error) {
		// Simulate a user env row that shadows a platform key.
		return []string{"SHINYHUB_APP_DATA=/evil"}, nil
	})

	p := process.StartParams{
		Slug:    "demo",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "1"},
		Port:    9999,
		Env:     []string{"SHINYHUB_APP_DATA=/legit"},
	}
	if _, err := m.Start(p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	last := lastValue(rt.lastEnv, "SHINYHUB_APP_DATA")
	if last != "/legit" {
		t.Fatalf("SHINYHUB_APP_DATA last value = %q, want /legit (platform wins)", last)
	}
}

func TestManager_Start_AppliesResolvedSharedMounts(t *testing.T) {
	// The manager's Start path must call the resolver and pass mounts through
	// to the runtime. captureRuntime records the SharedMounts received by Start.
	captured := make(chan []process.SharedMount, 1)

	rt := newCaptureRuntime(func(p process.StartParams) { captured <- p.SharedMounts })
	m := process.NewManager(t.TempDir(), rt)
	m.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
		return []process.SharedMount{{SourceSlug: "fetch", HostPath: t.TempDir()}}, nil
	})

	_, err := m.Start(process.StartParams{Slug: "consumer", Dir: t.TempDir(), Command: []string{"true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case mounts := <-captured:
		if len(mounts) != 1 || mounts[0].SourceSlug != "fetch" {
			t.Fatalf("expected one mount of 'fetch', got %+v", mounts)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime never received Start")
	}
}

// captureRuntime is a minimal Runtime implementation that invokes an onStart
// callback so tests can inspect the StartParams passed by the manager.
type captureRuntime struct {
	mu      sync.Mutex
	nextPID int
	stops   map[int]chan struct{}
	onStart func(process.StartParams)
}

func newCaptureRuntime(onStart func(process.StartParams)) *captureRuntime {
	return &captureRuntime{
		nextPID: 20000,
		stops:   make(map[int]chan struct{}),
		onStart: onStart,
	}
}

func (c *captureRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.RunHandle, error) {
	c.mu.Lock()
	pid := c.nextPID
	c.nextPID++
	c.stops[pid] = make(chan struct{})
	c.mu.Unlock()
	if c.onStart != nil {
		c.onStart(p)
	}
	return process.RunHandle{PID: pid}, nil
}

func (c *captureRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	c.mu.Lock()
	ch, ok := c.stops[h.PID]
	c.mu.Unlock()
	if ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (c *captureRuntime) Wait(_ context.Context, h process.RunHandle) error {
	c.mu.Lock()
	ch, ok := c.stops[h.PID]
	c.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (c *captureRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

func (c *captureRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

func (c *captureRuntime) HostPreparesDeps() bool { return true }

func (c *captureRuntime) AppBindHost() string { return "127.0.0.1" }

func lastValue(env []string, key string) string {
	out := ""
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = strings.TrimPrefix(kv, prefix)
		}
	}
	return out
}

func TestStart_NativeInjectsAppDataAndSymlink(t *testing.T) {
	appData := t.TempDir()
	bundle := t.TempDir()
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetAppDataRoot(appData)

	p := process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
		Port:    9999,
	}
	if _, err := m.Start(p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantPath := filepath.Join(appData, "demo")

	last := lastValue(rt.lastEnv, "SHINYHUB_APP_DATA")
	if last != wantPath {
		t.Errorf("SHINYHUB_APP_DATA = %q, want %q", last, wantPath)
	}

	target, err := os.Readlink(filepath.Join(bundle, "data"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != wantPath {
		t.Errorf("symlink target = %q, want %q", target, wantPath)
	}

	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("data dir missing: %v", err)
	}
}

func TestStart_RefusesIfBundleHasDataEntry(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "data"), []byte("squat"), 0o640); err != nil {
		t.Fatal(err)
	}
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetAppDataRoot(t.TempDir())

	_, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected data-conflict error, got %v", err)
	}
}

func TestStart_NoAppDataRootSkipsSymlinkAndEnv(t *testing.T) {
	bundle := t.TempDir()
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	// No SetAppDataRoot call — feature opt-out.

	p := process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
	}
	if _, err := m.Start(p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if v := lastValue(rt.lastEnv, "SHINYHUB_APP_DATA"); v != "" {
		t.Errorf("SHINYHUB_APP_DATA should not be set when appDataRoot is empty, got %q", v)
	}
	if _, err := os.Lstat(filepath.Join(bundle, "data")); !os.IsNotExist(err) {
		t.Errorf("symlink should not be created when appDataRoot is empty, lstat err = %v", err)
	}
}

func TestStart_IdempotentWhenSymlinkAlreadyPointsToCorrectTarget(t *testing.T) {
	appData := t.TempDir()
	bundle := t.TempDir()
	// Use the native runtime so Stop can actually terminate the process and
	// release the entry — fakeRuntime.Wait blocks forever.
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	m.SetAppDataRoot(appData)

	p := process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "10"},
		Port:    9999,
	}
	// First start creates the symlink.
	if _, err := m.Start(p); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Stop so the entry is released.
	if err := m.Stop("demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Second start with the same bundle dir must succeed (restart/wake path).
	if _, err := m.Start(p); err != nil {
		t.Fatalf("second Start (idempotent symlink): %v", err)
	}
	defer m.Stop("demo") //nolint:errcheck
	// Symlink still points to the correct target.
	target, err := os.Readlink(filepath.Join(bundle, "data"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if want := filepath.Join(appData, "demo"); target != want {
		t.Errorf("symlink target = %q, want %q", target, want)
	}
}

func TestStart_RefusesIfSymlinkPointsToWrongTarget(t *testing.T) {
	appData := t.TempDir()
	bundle := t.TempDir()
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetAppDataRoot(appData)

	// Plant a symlink pointing to a different location.
	wrongTarget := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(wrongTarget, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(wrongTarget, filepath.Join(bundle, "data")); err != nil {
		t.Fatal(err)
	}

	_, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected data-conflict error for foreign symlink, got %v", err)
	}
}

func TestStart_RefusesIfBundleHasDataDir(t *testing.T) {
	bundle := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bundle, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetAppDataRoot(t.TempDir())

	_, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected data-conflict error for pre-existing dir, got %v", err)
	}
}
