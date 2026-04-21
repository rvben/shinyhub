package deploy_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestExtractBundle(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	if err := createTestBundle(zipPath, map[string]string{
		"app.py":           "# shiny app",
		"requirements.txt": "shiny",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	if err := deploy.ExtractBundle(zipPath, destDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "app.py")); err != nil {
		t.Error("expected app.py to be extracted")
	}
}

func TestExtractBundle_ZipSlip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "malicious.zip")
	// Attempt path traversal via a ../../../etc/passwd-style entry name.
	if err := createTestBundle(zipPath, map[string]string{
		"../escape.txt": "should not appear outside destDir",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	err := deploy.ExtractBundle(zipPath, destDir)
	if err == nil {
		t.Fatal("expected error for zip-slip entry, got nil")
	}

	// The file must not have escaped to the parent of destDir.
	escaped := filepath.Join(dir, "escape.txt")
	if _, err := os.Stat(escaped); err == nil {
		t.Error("zip-slip: file escaped destDir — path traversal not prevented")
	}
}

func TestExtractBundle_RejectsPerEntryOverflow(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bomb.zip")
	// Single 2 MiB zero-filled entry; limit set to 1 MiB.
	if err := createBombBundle(zipPath, "bomb.bin", 2<<20); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	err := deploy.ExtractBundleWithLimits(zipPath, destDir, 1<<20, 10<<20)
	if err == nil {
		t.Fatal("expected error for per-entry overflow, got nil")
	}
	if !errors.Is(err, deploy.ErrBundleTooLarge) {
		t.Errorf("expected ErrBundleTooLarge, got %v", err)
	}
}

func TestExtractBundle_RejectsAggregateOverflow(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "aggregate.zip")
	// Three 400 KiB entries = 1.2 MiB total; each under the per-entry cap of
	// 1 MiB but combined exceeds the 1 MiB aggregate cap.
	files := map[string]string{
		"a.bin": strings.Repeat("x", 400<<10),
		"b.bin": strings.Repeat("x", 400<<10),
		"c.bin": strings.Repeat("x", 400<<10),
	}
	if err := createTestBundle(zipPath, files); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	err := deploy.ExtractBundleWithLimits(zipPath, destDir, 1<<20, 1<<20)
	if err == nil {
		t.Fatal("expected error for aggregate overflow, got nil")
	}
	if !errors.Is(err, deploy.ErrBundleTooLarge) {
		t.Errorf("expected ErrBundleTooLarge, got %v", err)
	}
}

func TestExtractBundle_WithinLimitsSucceeds(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "ok.zip")
	if err := createTestBundle(zipPath, map[string]string{
		"app.py":           "print('hi')",
		"requirements.txt": "shiny",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	if err := deploy.ExtractBundleWithLimits(zipPath, destDir, 1<<20, 10<<20); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "app.py")); err != nil {
		t.Error("expected app.py to be extracted")
	}
}

func TestAllocatePort(t *testing.T) {
	p1 := deploy.AllocatePort()
	p2 := deploy.AllocatePort()
	if p1 == p2 {
		t.Error("expected different ports")
	}
	if p1 < 20000 || p1 > 60000 {
		t.Errorf("port out of range: %d", p1)
	}
	if p2 < 20000 || p2 > 60000 {
		t.Errorf("p2 out of range: %d", p2)
	}
}

func TestAllocatePort_WrapAround(t *testing.T) {
	// Drive the counter to 60000, then verify the next call wraps back into range.
	deploy.SetPortCounterForTest(59999)
	p1 := deploy.AllocatePort() // 60000 — last valid
	p2 := deploy.AllocatePort() // should wrap to 20001 (or 20000 sentinel + 1)
	if p1 != 60000 {
		t.Errorf("expected 60000, got %d", p1)
	}
	if p2 < 20000 || p2 > 60000 {
		t.Errorf("wrapped port out of range: %d", p2)
	}
}

func TestDeploy_CommandOnly(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()

	dir := t.TempDir()

	params := deploy.Params{
		Slug:      "test-deploy",
		BundleDir: dir,
		Command:   []string{"sleep", "30"},
		Manager:   mgr,
		Proxy:     prx,
		HealthCheck: func(port int, timeout time.Duration) error {
			return nil // no HTTP server in this test
		},
	}
	info, err := deploy.Run(params)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer mgr.Stop("test-deploy")

	if len(info.Replicas) != 1 {
		t.Fatalf("want 1 replica, got %d", len(info.Replicas))
	}
	if info.Replicas[0].Port <= 0 {
		t.Errorf("expected valid port, got %d", info.Replicas[0].Port)
	}
	if info.Replicas[0].PID <= 0 {
		t.Errorf("expected valid PID, got %d", info.Replicas[0].PID)
	}
}

func TestRun_PoolBootsAllReplicas(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("pool-all")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "pool-all", BundleDir: bundle, Replicas: 3,
		Manager: mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(int, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 3 {
		t.Fatalf("want 3 replicas, got %d", len(result.Replicas))
	}
	if !prx.HasLiveReplica("pool-all") {
		t.Fatal("proxy has no live replica")
	}
}

func TestRun_PartialHealthStillSucceeds(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("pool-partial")
	prx := proxy.New()

	var failOnce atomic.Bool
	result, err := deploy.Run(deploy.Params{
		Slug: "pool-partial", BundleDir: bundle, Replicas: 2,
		Manager: mgr, Proxy: prx,
		Command: []string{"sleep", "30"},
		HealthCheck: func(port int, _ time.Duration) error {
			if failOnce.CompareAndSwap(false, true) {
				return fmt.Errorf("simulated")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 1 {
		t.Fatalf("want 1 healthy replica, got %d", len(result.Replicas))
	}
}

func TestRun_AllFailHealthErrors(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()

	_, err := deploy.Run(deploy.Params{
		Slug: "pool-allfail", BundleDir: bundle, Replicas: 2,
		Manager: mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(int, time.Duration) error { return fmt.Errorf("boom") },
	})
	if err == nil {
		t.Fatal("expected error when all replicas fail health")
	}
	if prx.HasLiveReplica("pool-allfail") {
		t.Fatal("proxy should not have any replica registered")
	}
}

func TestRunReplica_SingleBoot(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("one-rep")
	prx := proxy.New()
	prx.SetPoolSize("one-rep", 3)

	r, err := deploy.RunReplica(deploy.Params{
		Slug: "one-rep", BundleDir: bundle, Replicas: 3,
		Manager: mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(int, time.Duration) error { return nil },
	}, 2)
	if err != nil {
		t.Fatalf("run replica: %v", err)
	}
	if r.Index != 2 {
		t.Fatalf("want index 2, got %d", r.Index)
	}
}

// TestRun_DockerSkipsHostDepInstall proves the fix for a long-standing footgun:
// when the runtime is Docker, deploy.Run must not attempt to install bundle
// dependencies on the host (no `uv sync`, no `Rscript renv::restore`). Those
// commands belong inside the container; running them on the host pollutes the
// host environment and fails on hosts where uv/Rscript aren't even installed.
//
// We assert the contract by counting how often the package's host-prep hooks
// fire. With a Docker-mode manager they must be called zero times even when
// the bundle has a pyproject.toml / renv.lock that would normally trigger the
// install path.
func TestRun_DockerSkipsHostDepInstall(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var pyCalls, rCalls atomic.Int32
	restore := deploy.SetSyncHooksForTest(
		func(string) error { pyCalls.Add(1); return nil },
		func(string) error { rCalls.Add(1); return nil },
	)
	defer restore()

	mgr := process.NewManager(t.TempDir(), &fakeContainerRuntime{})

	_, err := deploy.Run(deploy.Params{
		Slug: "docker-app", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: proxy.New(),
		HealthCheck: func(int, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	defer mgr.Stop("docker-app")

	if got := pyCalls.Load(); got != 0 {
		t.Errorf("python sync hook fired %d times under docker runtime; want 0", got)
	}
	if got := rCalls.Load(); got != 0 {
		t.Errorf("R sync hook fired %d times under docker runtime; want 0", got)
	}
}

// TestRun_NativeStillRunsHostDepInstall is the symmetric guarantee: native
// runtimes still need on-host preparation (no container to cache deps inside),
// so the hook MUST be invoked exactly once per Run.
func TestRun_NativeStillRunsHostDepInstall(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var pyCalls atomic.Int32
	restore := deploy.SetSyncHooksForTest(
		func(string) error { pyCalls.Add(1); return nil },
		func(string) error { return nil },
	)
	defer restore()

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	_, err := deploy.Run(deploy.Params{
		Slug: "native-app", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: proxy.New(),
		HealthCheck: func(int, time.Duration) error { return nil },
		// Inject a benign command so we don't depend on uv being installed.
		Command: nil, // force the appType branch
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	defer mgr.Stop("native-app")

	if got := pyCalls.Load(); got != 1 {
		t.Errorf("python sync hook fired %d times under native runtime; want 1", got)
	}
}

func TestBuildRCommand_NoRenv(t *testing.T) {
	dir := t.TempDir()

	cmd := deploy.BuildRCommand(dir, 8080)
	if len(cmd) == 0 {
		t.Fatal("expected non-empty command")
	}
	if cmd[0] != "Rscript" {
		t.Errorf("expected Rscript as first arg, got %s", cmd[0])
	}
	full := strings.Join(cmd, " ")
	if !strings.Contains(full, "shiny::runApp") {
		t.Errorf("expected shiny::runApp in command: %s", full)
	}
	if !strings.Contains(full, "8080") {
		t.Errorf("expected port 8080 in command: %s", full)
	}
}

func TestDetectAppType_Python(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if deploy.DetectAppType(dir) != "python" {
		t.Error("expected python for app.py")
	}
}

func TestDetectAppType_R(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.R"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if deploy.DetectAppType(dir) != "r" {
		t.Error("expected r for app.R")
	}
}

func TestDetectAppType_Unknown(t *testing.T) {
	dir := t.TempDir()
	if deploy.DetectAppType(dir) != "" {
		t.Error("expected empty string for unknown app type")
	}
}

func TestExtractBundle_RejectsDataEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	if err := createTestBundle(zipPath, map[string]string{
		"app.R":      "ui <- fluidPage()\n",
		"data/x.csv": "a,b\n",
	}); err != nil {
		t.Fatal(err)
	}
	err := deploy.ExtractBundle(zipPath, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected data-rejection error, got %v", err)
	}
}

func TestExtractBundle_RejectsParquetAtRoot(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	if err := createTestBundle(zipPath, map[string]string{
		"app.R":        "x",
		"seed.parquet": "PAR1",
	}); err != nil {
		t.Fatal(err)
	}
	err := deploy.ExtractBundle(zipPath, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "seed.parquet") {
		t.Fatalf("expected extension-rejection error, got %v", err)
	}
}

func TestExtractBundle_SoftSkipsCacheDirs(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	if err := createTestBundle(zipPath, map[string]string{
		"app.R":               "x",
		".git/HEAD":           "ref",
		"__pycache__/x.pyc":  "p",
	}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := deploy.ExtractBundle(zipPath, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Errorf(".git/HEAD should have been skipped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app.R")); err != nil {
		t.Errorf("app.R should have been extracted: %v", err)
	}
}

func TestExtractBundle_RejectsDataDirEntryWithoutCreating(t *testing.T) {
	// Build a ZIP that contains an explicit "data/" directory entry (and no
	// file inside it). The extractor must reject and not create the dir.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("data/"); err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("app.R")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// Write the in-memory ZIP to a temp file because ExtractBundleWithLimits takes a file path.
	zipPath := filepath.Join(t.TempDir(), "data-dir.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	err = deploy.ExtractBundleWithLimits(zipPath, out, deploy.DefaultMaxEntrySize, deploy.DefaultMaxBundleSize)
	if err == nil {
		t.Fatal("expected rejection, got nil")
	}
	if !strings.Contains(err.Error(), "data") {
		t.Errorf("error should mention 'data': %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "data")); !os.IsNotExist(statErr) {
		t.Errorf("data/ directory should NOT have been created: %v", statErr)
	}
}

// fakeContainerRuntime is a minimal Runtime that reports itself as
// containerized (HostPreparesDeps == false). It implements just enough of the
// Runtime contract to let deploy.Run go through Manager.Start without ever
// touching real OS processes.
type fakeContainerRuntime struct{}

func (f *fakeContainerRuntime) HostPreparesDeps() bool { return false }
func (f *fakeContainerRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.RunHandle, error) {
	return process.RunHandle{ContainerID: fmt.Sprintf("fake-%s-%d", p.Slug, p.Index)}, nil
}
func (f *fakeContainerRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (f *fakeContainerRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (f *fakeContainerRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (f *fakeContainerRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

func TestResolveResourceLimits(t *testing.T) {
	zero := 0
	pos := 256

	tests := []struct {
		name       string
		perApp     *int
		defaultVal int
		want       int
	}{
		{"nil uses default", nil, 512, 512},
		{"nil with zero default", nil, 0, 0},
		{"zero overrides default", &zero, 512, 0},
		{"positive overrides default", &pos, 512, 256},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deploy.ResolveMemoryLimitMB(tc.perApp, tc.defaultVal); got != tc.want {
				t.Errorf("ResolveMemoryLimitMB: got %d, want %d", got, tc.want)
			}
			if got := deploy.ResolveCPUQuotaPercent(tc.perApp, tc.defaultVal); got != tc.want {
				t.Errorf("ResolveCPUQuotaPercent: got %d, want %d", got, tc.want)
			}
		})
	}
}
