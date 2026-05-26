package process_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

func (f *fakeRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirror the real-runtime contract: SHINYHUB_APP_DATA is injected by the
	// Runtime from p.AppDataPath (NativeRuntime appends the host path;
	// DockerRuntime translates to the in-container path). Tests that assert
	// the cross-layer contract via lastEnv rely on this mirror.
	f.lastEnv = append([]string(nil), p.Env...)
	if p.AppDataPath != "" {
		f.lastEnv = append(f.lastEnv, "SHINYHUB_APP_DATA="+p.AppDataPath)
	}
	pid := f.nextPID
	f.nextPID++
	f.stops[pid] = make(chan struct{})
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
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

func (f *fakeRuntime) HostPreparesDeps() bool    { return true }
func (f *fakeRuntime) AppBindHost() string       { return "127.0.0.1" }
func (f *fakeRuntime) HostProvidesAppData() bool { return true }

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

// TestManagerStopAll verifies StopAll terminates every tracked app across
// slugs, backing the server.shutdown_apps=stop path.
func TestManagerStopAll(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	starts := []struct {
		slug string
		port int
	}{{"app-a", 19010}, {"app-b", 19011}}
	var pids []int
	for _, s := range starts {
		info, err := m.Start(process.StartParams{
			Slug:    s.slug,
			Dir:     t.TempDir(),
			Command: []string{"sleep", "30"},
			Port:    s.port,
		})
		if err != nil {
			t.Fatalf("start %s: %v", s.slug, err)
		}
		pids = append(pids, info.PID)
	}
	time.Sleep(100 * time.Millisecond)

	if err := m.StopAll(); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	for i, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("%s (pid %d) still alive after StopAll", starts[i].slug, pid)
		}
	}

	// StopAll on an empty manager must be a no-op, not an error.
	if err := m.StopAll(); err != nil {
		t.Errorf("StopAll on empty manager: %v", err)
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
	if got := m.AppBindHostFor(process.DefaultTier); got != "127.0.0.1" {
		t.Errorf("Manager.AppBindHostFor (native) = %q, want 127.0.0.1", got)
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

// TestStart_PlatformDefaultsLoseToUserEnv asserts the ordering contract for
// the OTEL_* injection path: platform defaults from the resolver are prepended
// BEFORE user env, so any user-supplied OTEL_* (or other override) wins under
// last-occurrence-wins semantics.
func TestStart_PlatformDefaultsLoseToUserEnv(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetPlatformDefaultEnvResolver(func(slug string, replica int) []string {
		return []string{
			"OTEL_EXPORTER_OTLP_ENDPOINT=http://platform:4318",
			"OTEL_SERVICE_NAME=" + slug,
		}
	})
	m.SetEnvResolver(func(slug string) ([]string, error) {
		// User wants a different OTLP endpoint for this app.
		return []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://user-collector:4318"}, nil
	})

	p := process.StartParams{
		Slug:    "demo",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "1"},
		Port:    9999,
	}
	if _, err := m.Start(p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := lastValue(rt.lastEnv, "OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://user-collector:4318" {
		t.Errorf("user env should win over platform default: got %q", got)
	}
	// Platform-only keys still flow through when not overridden.
	if got := lastValue(rt.lastEnv, "OTEL_SERVICE_NAME"); got != "demo" {
		t.Errorf("platform-only OTEL_SERVICE_NAME = %q, want demo", got)
	}
}

// TestStart_PlatformDefaultsReceiveSlugAndReplica verifies the resolver is
// called with the actual slug and replica index from StartParams.
func TestStart_PlatformDefaultsReceiveSlugAndReplica(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	var gotSlug string
	var gotReplica int
	m.SetPlatformDefaultEnvResolver(func(slug string, replica int) []string {
		gotSlug = slug
		gotReplica = replica
		return nil
	})
	if _, err := m.Start(process.StartParams{
		Slug:    "myapp",
		Index:   3,
		Dir:     t.TempDir(),
		Command: []string{"sleep", "1"},
		Port:    9999,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if gotSlug != "myapp" || gotReplica != 3 {
		t.Errorf("resolver received (%q, %d), want (myapp, 3)", gotSlug, gotReplica)
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

func (c *captureRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	c.mu.Lock()
	pid := c.nextPID
	c.nextPID++
	c.stops[pid] = make(chan struct{})
	c.mu.Unlock()
	if c.onStart != nil {
		c.onStart(p)
	}
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
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

func (c *captureRuntime) HostPreparesDeps() bool    { return true }
func (c *captureRuntime) AppBindHost() string       { return "127.0.0.1" }
func (c *captureRuntime) HostProvidesAppData() bool { return true }

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
	if err := m.SetAppDataRoot(appData); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

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
	if err := m.SetAppDataRoot(t.TempDir()); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

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
	if err := m.SetAppDataRoot(appData); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

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
	if err := m.SetAppDataRoot(appData); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

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

// TestStart_NormalizesRelativeAppDataRoot guards against a regression where a
// relative storage.app_data_dir produced a self-referential symlink at
// <bundle>/data: the kernel resolves a relative symlink target against the
// symlink's parent dir, so a target string like "data/app-data/demo" placed at
// <bundle>/data resolves back through the symlink itself ("Too many levels of
// symbolic links"). The Manager must normalize appDataRoot to an absolute path
// so both the symlink target and SHINYHUB_APP_DATA are unambiguous.
func TestStart_NormalizesRelativeAppDataRoot(t *testing.T) {
	root := t.TempDir()
	// Chdir so we can pass a relative path and still know the absolute form.
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	relAppData := "./data/app-data"
	if err := os.MkdirAll(relAppData, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	if err := m.SetAppDataRoot(relAppData); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

	if _, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
		Port:    9999,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	linkPath := filepath.Join(bundle, "data")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("symlink target = %q, want absolute path", target)
	}
	// The symlink must actually resolve; the regression made <bundle>/data
	// loop into itself, so os.Stat would return ELOOP.
	if _, err := os.Stat(linkPath); err != nil {
		t.Errorf("stat through symlink failed (likely self-referential loop): %v", err)
	}

	envVal := lastValue(rt.lastEnv, "SHINYHUB_APP_DATA")
	if !filepath.IsAbs(envVal) {
		t.Errorf("SHINYHUB_APP_DATA = %q, want absolute path", envVal)
	}
}

// TestSetAppDataRoot_NormalizesAtSetTimeNotStartTime locks in the design
// choice that path normalization happens at SetAppDataRoot, not at Start. If
// the server's working directory changes between configuration and the first
// Start (e.g. a background goroutine chdirs, or a future refactor moves
// filepath.Abs into Start), a relative path captured at set-time would
// silently start resolving against the wrong CWD. Normalizing at set-time
// makes that impossible.
func TestSetAppDataRoot_NormalizesAtSetTimeNotStartTime(t *testing.T) {
	originalCWD := t.TempDir()
	otherCWD := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	if err := os.Chdir(originalCWD); err != nil {
		t.Fatalf("chdir original: %v", err)
	}
	// Capture the canonical CWD after chdir. On macOS /var is a symlink to
	// /private/var, so t.TempDir()'s "/var/..." differs from Getwd()'s
	// "/private/var/..." — the latter is what filepath.Abs will produce.
	canonicalOriginal, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd original: %v", err)
	}
	relAppData := "./data/app-data"
	if err := os.MkdirAll(relAppData, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	if err := m.SetAppDataRoot(relAppData); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

	// Move the CWD *after* configuration. A correct implementation captured
	// the absolute path at set-time and is immune; a buggy one that defers
	// resolution to Start would now resolve against otherCWD.
	if err := os.Chdir(otherCWD); err != nil {
		t.Fatalf("chdir other: %v", err)
	}

	bundle := filepath.Join(otherCWD, "bundle")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if _, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
		Port:    9999,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	target, err := os.Readlink(filepath.Join(bundle, "data"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	wantPrefix := filepath.Join(canonicalOriginal, "data", "app-data")
	if !strings.HasPrefix(target, wantPrefix) {
		t.Errorf("symlink target = %q, want path rooted at original CWD %q (normalization deferred to Start?)", target, wantPrefix)
	}
	if _, err := os.Stat(filepath.Join(bundle, "data")); err != nil {
		t.Errorf("stat through symlink failed: %v", err)
	}
}

func TestSetAppDataRoot_EmptyStringDisablesFeature(t *testing.T) {
	m := process.NewManager(t.TempDir(), newFakeRuntime())
	if err := m.SetAppDataRoot(""); err != nil {
		t.Fatalf("SetAppDataRoot(\"\"): %v", err)
	}
}

func TestStart_RefusesIfBundleHasDataDir(t *testing.T) {
	bundle := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bundle, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	if err := m.SetAppDataRoot(t.TempDir()); err != nil {
		t.Fatalf("SetAppDataRoot: %v", err)
	}

	_, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     bundle,
		Command: []string{"sleep", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected data-conflict error for pre-existing dir, got %v", err)
	}
}

func TestManagerProvisionsDataDirViaVolume(t *testing.T) {
	root := t.TempDir()
	bundle := t.TempDir()
	m := process.NewManager(t.TempDir(), newFakeRuntime())
	if err := m.SetAppDataRoot(root); err != nil {
		t.Fatalf("set root: %v", err)
	}
	if _, err := m.Start(process.StartParams{
		Slug: "v", Index: 0, Dir: bundle, Command: []string{"x"}, Port: 1,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "v")); err != nil || !fi.IsDir() {
		t.Fatalf("expected data dir %s: err=%v", filepath.Join(root, "v"), err)
	}
	if _, err := os.Lstat(filepath.Join(bundle, "data")); err != nil {
		t.Fatalf("expected data symlink in bundle: %v", err)
	}
	target, err := os.Readlink(filepath.Join(bundle, "data"))
	if err != nil {
		t.Fatalf("readlink data symlink: %v", err)
	}
	if want := filepath.Join(root, "v"); target != want {
		t.Errorf("symlink target = %q, want %q", target, want)
	}
}

// fakeRemoteRuntime is a Runtime that does not provide host app data,
// used to prove Manager.Start skips host-side provisioning for remote tiers.
type fakeRemoteRuntime struct {
	startParams process.StartParams
}

func (f *fakeRemoteRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	f.startParams = p
	return process.ReplicaEndpoint{URL: "https://worker.example/v1/data/tok", Provider: "remote_docker"}, nil
}
func (f *fakeRemoteRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (f *fakeRemoteRuntime) Wait(context.Context, process.RunHandle) error  { return nil }
func (f *fakeRemoteRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (f *fakeRemoteRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (f *fakeRemoteRuntime) HostPreparesDeps() bool    { return false }
func (f *fakeRemoteRuntime) AppBindHost() string       { return "0.0.0.0" }
func (f *fakeRemoteRuntime) HostProvidesAppData() bool { return false }

func TestManagerStart_RemoteRuntimeSkipsHostAppData(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRemoteRuntime{}
	m := process.NewManager(dir, rt)
	if err := m.SetAppDataRoot(filepath.Join(dir, "app-data")); err != nil {
		t.Fatal(err)
	}
	// The resolver supplies the mount with a non-empty HostPath; stripping must
	// happen after resolution, not before, so we prove the ordering here.
	m.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
		return []process.SharedMount{{SourceSlug: "shared", HostPath: filepath.Join(dir, "resolved-host-path")}}, nil
	})

	bundleDir := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.Start(process.StartParams{
		Slug:    "app",
		Index:   0,
		Dir:     bundleDir,
		Command: []string{"run"},
		Port:    8080,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// No app-data dir should have been provisioned on the host.
	if _, statErr := os.Stat(filepath.Join(dir, "app-data", "app")); !os.IsNotExist(statErr) {
		t.Errorf("host app-data dir was provisioned for a remote runtime: %v", statErr)
	}
	// No data symlink should have been created in the bundle.
	if _, statErr := os.Lstat(filepath.Join(bundleDir, "data")); !os.IsNotExist(statErr) {
		t.Errorf("data symlink created for a remote runtime")
	}
	// HostPath must be cleared after resolution; only SourceSlug survives for remote resolution.
	if got := rt.startParams.AppDataPath; got != "" {
		t.Errorf("AppDataPath = %q, want empty for remote runtime", got)
	}
	if len(rt.startParams.SharedMounts) != 1 {
		t.Fatalf("SharedMounts len = %d, want 1", len(rt.startParams.SharedMounts))
	}
	if hp := rt.startParams.SharedMounts[0].HostPath; hp != "" {
		t.Errorf("SharedMount.HostPath = %q, want empty for remote runtime", hp)
	}
	if ss := rt.startParams.SharedMounts[0].SourceSlug; ss != "shared" {
		t.Errorf("SharedMount.SourceSlug = %q, want \"shared\"", ss)
	}
}

func TestManagerDispatchesByTier(t *testing.T) {
	def := newFakeRuntime()
	burst := newFakeRuntime()
	burst.nextPID = 50000 // distinct PID range proves which runtime started it

	m := process.NewManager(t.TempDir(), def)
	m.RegisterRuntime("burst", burst)

	// Default tier (empty Tier => "local") uses def.
	localInfo, err := m.Start(process.StartParams{
		Slug: "a", Index: 0, Command: []string{"x"}, Port: 1,
	})
	if err != nil {
		t.Fatalf("start local: %v", err)
	}
	if localInfo.PID < 10000 || localInfo.PID >= 50000 {
		t.Fatalf("local replica got PID %d; expected default-runtime range", localInfo.PID)
	}
	if localInfo.Tier != process.DefaultTier {
		t.Errorf("local replica Tier = %q, want %q", localInfo.Tier, process.DefaultTier)
	}

	// Explicit burst tier uses burst.
	burstInfo, err := m.Start(process.StartParams{
		Slug: "b", Index: 0, Command: []string{"x"}, Port: 2, Tier: "burst",
	})
	if err != nil {
		t.Fatalf("start burst: %v", err)
	}
	if burstInfo.PID < 50000 {
		t.Fatalf("burst replica got PID %d; expected burst-runtime range", burstInfo.PID)
	}
	if burstInfo.Tier != "burst" {
		t.Errorf("burst replica Tier = %q, want %q", burstInfo.Tier, "burst")
	}

	// Stop dispatches to the owning runtime without panicking.
	if err := m.StopReplica("a", 0); err != nil {
		t.Fatalf("stop local: %v", err)
	}
	if err := m.StopReplica("b", 0); err != nil {
		t.Fatalf("stop burst: %v", err)
	}
}
