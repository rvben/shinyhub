package process_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeRuntime is a minimal Runtime stub that captures the env passed to Start.
type fakeRuntime struct {
	lastEnv []string
}

func (r *fakeRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.RunHandle, error) {
	r.lastEnv = p.Env
	return process.RunHandle{PID: 1}, nil
}

func (r *fakeRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }

func (r *fakeRuntime) Wait(_ context.Context, _ process.RunHandle) error {
	select {}
}

func (r *fakeRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := m.Status("crash-test")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if info.Status == process.StatusCrashed {
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

	gotHandle, ok := m.Handle("adopted")
	if !ok {
		t.Fatal("Handle: not found")
	}
	if gotHandle != handle {
		t.Errorf("expected handle %+v, got %+v", handle, gotHandle)
	}
}

func TestStart_PlatformEnvWinsOverUserEnv(t *testing.T) {
	rt := &fakeRuntime{}
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
	rt := &fakeRuntime{}
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
	rt := &fakeRuntime{}
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
	rt := &fakeRuntime{}
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
