package deploy_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	// Fake bindability so the wraparound logic is exercised deterministically:
	// a real bind probe can spuriously fail if the test machine happens to
	// have the candidate port (e.g. 60000) held by an unrelated process,
	// which made this test flaky when it probed real OS ports.
	restore := deploy.SetPortIsBindableForTest(func(int) bool { return true })
	defer restore()

	// Drive the counter to 60000, then verify the next call wraps back into range.
	deploy.SetPortCounterForTest(59999)
	p1 := deploy.AllocatePort() // 60000 — last valid
	p2 := deploy.AllocatePort() // wraps to 20001
	if p1 != 60000 {
		t.Errorf("expected 60000, got %d", p1)
	}
	if p2 != 20001 {
		t.Errorf("expected wraparound to 20001, got %d", p2)
	}
}

// TestAllocatePort_SkipsInUsePorts pins down the contract that motivated the
// rewrite: a port already held by another listener (e.g. a survivor container
// from a prior shinyhub process) must NOT be handed back. Without the bind
// probe, a counter reset on restart would happily re-issue an in-use port and
// the spawned app would bind-fail.
func TestAllocatePort_SkipsInUsePorts(t *testing.T) {
	// Reserve the next port the counter will issue.
	deploy.SetPortCounterForTest(40000)
	occupied := 40001
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", occupied))
	if err != nil {
		t.Skipf("could not bind probe port %d: %v", occupied, err)
	}
	defer l.Close()

	got := deploy.AllocatePort()
	if got == occupied {
		t.Errorf("AllocatePort returned in-use port %d; expected the probe to skip it", got)
	}
	if got < 20000 || got > 60000 {
		t.Errorf("port out of range: %d", got)
	}
}

// TestAllocatePort_ConcurrentIsRaceFree verifies that concurrent allocations
// never produce duplicates within a single burst — the property a long-lived
// deploys-and-restarts workload depends on.
func TestAllocatePort_ConcurrentIsRaceFree(t *testing.T) {
	const N = 64
	deploy.SetPortCounterForTest(45000)

	results := make(chan int, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- deploy.AllocatePort()
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[int]struct{}, N)
	for p := range results {
		if _, dup := seen[p]; dup {
			t.Errorf("duplicate port %d returned by concurrent AllocatePort", p)
		}
		seen[p] = struct{}{}
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
		HealthCheck: func(_ string, _ time.Duration, _ http.RoundTripper) error {
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
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
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

// TestRun_HealthTimeoutFromManifest verifies that [app] startup_timeout_seconds
// in the bundle manifest lengthens the readiness deadline the health check is
// given, so a slow-but-correct startup within that window deploys successfully.
func TestRun_HealthTimeoutFromManifest(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "shinyhub.toml"),
		[]byte("[app]\nstartup_timeout_seconds = 600\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("tmo-manifest")
	prx := proxy.New()

	var mu sync.Mutex
	var gotTimeout time.Duration
	_, err := deploy.Run(deploy.Params{
		Slug: "tmo-manifest", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: prx,
		Command: []string{"sleep", "30"},
		HealthCheck: func(_ string, to time.Duration, _ http.RoundTripper) error {
			mu.Lock()
			gotTimeout = to
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	got := gotTimeout
	mu.Unlock()
	if got != 600*time.Second {
		t.Fatalf("health timeout = %v, want 600s from manifest startup_timeout_seconds", got)
	}
}

// TestRun_HealthTimeoutDefaultsWithoutManifest verifies the readiness deadline
// falls back to the platform default (120s) when no startup_timeout_seconds is
// declared, so existing bundles keep today's behaviour.
func TestRun_HealthTimeoutDefaultsWithoutManifest(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("tmo-default")
	prx := proxy.New()

	var mu sync.Mutex
	var gotTimeout time.Duration
	_, err := deploy.Run(deploy.Params{
		Slug: "tmo-default", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: prx,
		Command: []string{"sleep", "30"},
		HealthCheck: func(_ string, to time.Duration, _ http.RoundTripper) error {
			mu.Lock()
			gotTimeout = to
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	got := gotTimeout
	mu.Unlock()
	if got != 120*time.Second {
		t.Fatalf("health timeout = %v, want the 120s platform default", got)
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
		HealthCheck: func(_ string, _ time.Duration, _ http.RoundTripper) error {
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
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return fmt.Errorf("boom") },
	})
	if err == nil {
		t.Fatal("expected error when all replicas fail health")
	}
	if prx.HasLiveReplica("pool-allfail") {
		t.Fatal("proxy should not have any replica registered")
	}
}

func TestRun_PlacementAssignsTiersOverGlobalIndex(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("burst", process.NewNativeRuntime())
	defer mgr.Stop("placed")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "placed", BundleDir: bundle,
		Placement:   map[string]int{"local": 1, "burst": 2},
		TierOrder:   []string{"local", "burst"},
		DefaultTier: "local",
		Manager:     mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 3 {
		t.Fatalf("want 3 replicas, got %d", len(result.Replicas))
	}
	tierByIndex := map[int]string{}
	for _, r := range result.Replicas {
		tierByIndex[r.Index] = r.Tier
	}
	if tierByIndex[0] != "local" {
		t.Errorf("index 0 tier = %q, want local", tierByIndex[0])
	}
	if tierByIndex[1] != "burst" || tierByIndex[2] != "burst" {
		t.Errorf("indices 1,2 tiers = %q,%q, want burst,burst", tierByIndex[1], tierByIndex[2])
	}
}

func TestRun_EmptyPlacementUsesDefaultTier(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("default-tier")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "default-tier", BundleDir: bundle, Replicas: 2,
		Manager: mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 2 {
		t.Fatalf("want 2 replicas, got %d", len(result.Replicas))
	}
	for _, r := range result.Replicas {
		if r.Tier != process.DefaultTier {
			t.Errorf("replica %d tier = %q, want %q", r.Index, r.Tier, process.DefaultTier)
		}
	}
}

func TestRunReplica_RestartsOnPlacedTier(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("burst", process.NewNativeRuntime())
	defer mgr.Stop("restart-placed")
	prx := proxy.New()
	prx.SetPoolSize("restart-placed", 3)

	// Index 2 belongs to the burst tier under this placement; a crash-restart of
	// that index must land on burst, not the default tier.
	r, err := deploy.RunReplica(deploy.Params{
		Slug: "restart-placed", BundleDir: bundle,
		Placement:   map[string]int{"local": 1, "burst": 2},
		TierOrder:   []string{"local", "burst"},
		DefaultTier: "local",
		Manager:     mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}, 2)
	if err != nil {
		t.Fatalf("run replica: %v", err)
	}
	if r.Tier != "burst" {
		t.Errorf("restarted replica 2 tier = %q, want burst", r.Tier)
	}
}

// TestRunReplica_HonorsColocateWorkers asserts a single-replica restart of a
// shared-mount consumer pins to a worker from the colocation set instead of
// self-placing by least load. The watchdog re-places crashed/lost replicas one
// at a time through RunReplica; if it ignored the pin, a recovered replica could
// land on a worker that does not host the mounted source data.
func TestRunReplica_HonorsColocateWorkers(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	// The runtime would self-place across both workers; the pin must confine the
	// restarted replica to w-b.
	mgr.RegisterRuntime("remote", &placerRuntime{workers: []string{"w-a", "w-b"}})
	defer mgr.Stop("reheal")
	prx := proxy.New()
	prx.SetPoolSize("reheal", 2)

	r, err := deploy.RunReplica(deploy.Params{
		Slug: "reheal", BundleDir: bundle,
		Placement:       map[string]int{"remote": 2},
		TierOrder:       []string{"remote"},
		DefaultTier:     "remote",
		ColocateWorkers: []string{"w-b"},
		Manager:         mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}, 0)
	if err != nil {
		t.Fatalf("run replica: %v", err)
	}
	if r.WorkerID != "w-b" {
		t.Errorf("restarted replica on worker %q, want w-b (colocation pin ignored)", r.WorkerID)
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
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}, 2)
	if err != nil {
		t.Fatalf("run replica: %v", err)
	}
	if r.Index != 2 {
		t.Fatalf("want index 2, got %d", r.Index)
	}
}

// TestRun_RunsPostDeployHooksBeforeReplicaBoot verifies the hook fires
// between dependency installation and replica start: the order matters
// because hooks typically need the venv that `uv sync` just populated, and
// they must complete before the app process binds the port.
func TestRun_RunsPostDeployHooksBeforeReplicaBoot(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(`
[[hook]]
on = "post-deploy"
command = ["python", "-m", "scripts.migrate"]
`), 0644); err != nil {
		t.Fatal(err)
	}

	var (
		mu       sync.Mutex
		events   []string
		hookSeen bool
	)
	restore := deploy.SetHookRunnerForTest(func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		mu.Lock()
		defer mu.Unlock()
		hookSeen = true
		events = append(events, "hook:"+argv[len(argv)-1])
		return nil
	})
	defer restore()

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("hook-app")
	prx := proxy.New()

	_, err := deploy.Run(deploy.Params{
		Slug: "hook-app", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: prx,
		Command: []string{"sleep", "30"},
		HealthCheck: func(_ string, _ time.Duration, _ http.RoundTripper) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "boot")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}

	if !hookSeen {
		t.Fatal("post-deploy hook was not invoked")
	}
	// Order: hook(s) must fire before any replica boot/health check.
	if len(events) < 2 || events[0] != "hook:scripts.migrate" {
		t.Errorf("event order = %v; want hook before boot", events)
	}
}

// TestRun_HookFailureAbortsDeploy proves a broken hook short-circuits the
// deploy and propagates the error — silently starting the app when setup
// failed is the dangerous outcome this guards against.
func TestRun_HookFailureAbortsDeploy(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(`
[[hook]]
on = "post-deploy"
command = ["broken"]
`), 0644); err != nil {
		t.Fatal(err)
	}

	restore := deploy.SetHookRunnerForTest(func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		return errors.New("migration crashed")
	})
	defer restore()

	prx := proxy.New()
	_, err := deploy.Run(deploy.Params{
		Slug: "broken-hook", BundleDir: bundle, Replicas: 1,
		Manager: process.NewManager(t.TempDir(), process.NewNativeRuntime()),
		Proxy:   prx,
		Command: []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error {
			t.Error("replica boot should not be reached when post-deploy hook fails")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "migration crashed") {
		t.Errorf("expected hook error to propagate, got %v", err)
	}
	if prx.HasLiveReplica("broken-hook") {
		t.Error("proxy must not register replicas after hook failure")
	}
}

// TestRun_HookSkippedUnderContainerRuntime: hooks expect host-side venv
// state, which doesn't exist when the container runtime prepares deps
// inside the image. The hook must be skipped (not failed) so users can
// adopt docker mode without removing their hooks from shinyhub.toml.
func TestRun_HookSkippedUnderContainerRuntime(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(`
[[hook]]
on = "post-deploy"
command = ["should-not-run"]
`), 0644); err != nil {
		t.Fatal(err)
	}

	var called atomic.Bool
	restore := deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		called.Store(true)
		return nil
	})
	defer restore()

	_, err := deploy.Run(deploy.Params{
		Slug: "docker-hook", BundleDir: bundle, Replicas: 1,
		Manager:     process.NewManager(t.TempDir(), &fakeContainerRuntime{}),
		Proxy:       proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if called.Load() {
		t.Error("post-deploy hook ran under container runtime; should have been skipped")
	}
}

// TestRun_HookSkippedSurfacedInResult: skipping post-deploy hooks under a
// container runtime is invisible to the developer when it lives only in the
// server log. Run must report how many hooks it skipped so the API can relay
// it and the CLI can warn the developer their hooks did not execute.
func TestRun_HookSkippedSurfacedInResult(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(`
[[hook]]
on = "post-deploy"
command = ["should-not-run"]

[[hook]]
on = "post-deploy"
command = ["also-skipped"]
`), 0644); err != nil {
		t.Fatal(err)
	}

	restore := deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		return nil
	})
	defer restore()

	res, err := deploy.Run(deploy.Params{
		Slug: "docker-hook-count", BundleDir: bundle, Replicas: 1,
		Manager:     process.NewManager(t.TempDir(), &fakeContainerRuntime{}),
		Proxy:       proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if res.HooksSkipped != 2 {
		t.Errorf("HooksSkipped = %d, want 2", res.HooksSkipped)
	}
}

// TestRun_NativeRuntimeReportsNoSkippedHooks: when the host prepares deps the
// hooks run, so nothing is reported as skipped.
func TestRun_NativeRuntimeReportsNoSkippedHooks(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(`
[[hook]]
on = "post-deploy"
command = ["echo", "ok"]
`), 0644); err != nil {
		t.Fatal(err)
	}

	var called atomic.Bool
	restore := deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		called.Store(true)
		return nil
	})
	defer restore()

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	res, err := deploy.Run(deploy.Params{
		Slug: "native-hook", BundleDir: bundle, Replicas: 1,
		Manager:     mgr,
		Proxy:       proxy.New(),
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	defer mgr.Stop("native-hook")
	if !called.Load() {
		t.Error("post-deploy hook should run under native runtime")
	}
	if res.HooksSkipped != 0 {
		t.Errorf("HooksSkipped = %d, want 0", res.HooksSkipped)
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
		func(context.Context, string, []string) error { pyCalls.Add(1); return nil },
		func(context.Context, string, []string) error { rCalls.Add(1); return nil },
	)
	defer restore()

	mgr := process.NewManager(t.TempDir(), &fakeContainerRuntime{})

	_, err := deploy.Run(deploy.Params{
		Slug: "docker-app", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
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
	// Command must be nil to exercise the appType branch in resolveBootParams,
	// which is where the python sync hook fires. bootReplica then derives
	// `uv run …` from the bundle, so the test process needs uv on PATH.
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH — skipping native dep-install test")
	}

	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var pyCalls atomic.Int32
	restore := deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { pyCalls.Add(1); return nil },
		func(context.Context, string, []string) error { return nil },
	)
	defer restore()

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	_, err := deploy.Run(deploy.Params{
		Slug: "native-app", BundleDir: bundle, Replicas: 1,
		Manager: mgr, Proxy: proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
		Command:     nil,
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

	cmd := deploy.BuildRCommand(dir, 8080, "127.0.0.1")
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
	if !strings.Contains(full, "host='127.0.0.1'") {
		t.Errorf("expected host='127.0.0.1' in command: %s", full)
	}
}

// TestBuildRCommand_HonorsBindHost verifies the bind host is propagated into
// the R startup expression — required for Docker bridge mode where the app
// must listen on 0.0.0.0 inside the container so the published port works.
func TestBuildRCommand_HonorsBindHost(t *testing.T) {
	cmd := deploy.BuildRCommand(t.TempDir(), 8080, "0.0.0.0")
	full := strings.Join(cmd, " ")
	if !strings.Contains(full, "host='0.0.0.0'") {
		t.Errorf("expected host='0.0.0.0' in command: %s", full)
	}
	if strings.Contains(full, "127.0.0.1") {
		t.Errorf("did not expect 127.0.0.1 when bindHost is 0.0.0.0: %s", full)
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
		"app.R":             "x",
		".git/HEAD":         "ref",
		"__pycache__/x.pyc": "p",
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

func (f *fakeContainerRuntime) HostPreparesDeps() bool    { return false }
func (f *fakeContainerRuntime) AppBindHost() string       { return "0.0.0.0" }
func (f *fakeContainerRuntime) HostProvidesAppData() bool { return true }
func (f *fakeContainerRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	id := fmt.Sprintf("fake-%s-%d", p.Slug, p.Index)
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "docker",
		WorkerID: id,
		Handle:   process.RunHandle{ContainerID: id},
	}, nil
}
func (f *fakeContainerRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (f *fakeContainerRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (f *fakeContainerRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (f *fakeContainerRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

// placerRuntime is a Runtime that also implements process.ReplicaPlacer. It
// hands deploy a pre-planned worker per replica (round-robin over its worker
// set) and echoes whatever TargetWorker each Start receives back as the
// replica's WorkerID, so a test can assert the pre-planned assignment actually
// reached Manager.Start instead of every replica self-placing.
type placerRuntime struct {
	workers []string
}

func (p *placerRuntime) PlanPlacement(_ string, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, p.workers[i%len(p.workers)])
	}
	return out
}
func (p *placerRuntime) HostPreparesDeps() bool    { return false }
func (p *placerRuntime) AppBindHost() string       { return "0.0.0.0" }
func (p *placerRuntime) HostProvidesAppData() bool { return false }
func (p *placerRuntime) Start(_ context.Context, sp process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", sp.Port),
		Provider: "remote_docker",
		WorkerID: sp.TargetWorker,
		Handle:   process.RunHandle{ContainerID: sp.TargetWorker + "/c-" + fmt.Sprint(sp.Index)},
	}, nil
}
func (p *placerRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (p *placerRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (p *placerRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (p *placerRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

// TestRun_PreplansWorkerSpreadAcrossPool asserts that a concurrent multi-replica
// pool boot spreads across the tier's workers: deploy pre-plans worker
// assignments up front and threads each chosen worker into the replica's Start,
// so two replicas land on two different workers. Without pre-planning, every
// replica would self-place against the same pre-deploy snapshot and stack onto
// one worker.
func TestRun_PreplansWorkerSpreadAcrossPool(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", &placerRuntime{workers: []string{"w-a", "w-b"}})
	defer mgr.Stop("spread")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "spread", BundleDir: bundle,
		Placement:   map[string]int{"remote": 2},
		TierOrder:   []string{"remote"},
		DefaultTier: "remote",
		Manager:     mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 2 {
		t.Fatalf("want 2 replicas, got %d", len(result.Replicas))
	}
	workers := map[string]int{}
	for _, r := range result.Replicas {
		if r.WorkerID == "" {
			t.Fatalf("replica %d has empty WorkerID: deploy did not thread a pre-planned target", r.Index)
		}
		workers[r.WorkerID]++
	}
	if len(workers) != 2 {
		t.Fatalf("pool co-located on %d worker(s): %v; want spread across both workers", len(workers), workers)
	}
}

// TestRun_ColocateWorkersPinsPool asserts that ColocateWorkers overrides
// least-loaded placement: every replica is pinned to a worker from the
// colocation set even though the runtime's own PlanPlacement would round-robin
// across all of the tier's workers. This is how a shared-mount consumer is kept
// on a worker that also hosts its source's data.
func TestRun_ColocateWorkersPinsPool(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	// The runtime can place on either worker; the colocation pin must confine
	// the whole pool to w-b regardless.
	mgr.RegisterRuntime("remote", &placerRuntime{workers: []string{"w-a", "w-b"}})
	defer mgr.Stop("pinned")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "pinned", BundleDir: bundle,
		Placement:       map[string]int{"remote": 3},
		TierOrder:       []string{"remote"},
		DefaultTier:     "remote",
		ColocateWorkers: []string{"w-b"},
		Manager:         mgr, Proxy: prx,
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Replicas) != 3 {
		t.Fatalf("want 3 replicas, got %d", len(result.Replicas))
	}
	for _, r := range result.Replicas {
		if r.WorkerID != "w-b" {
			t.Errorf("replica %d on worker %q, want w-b (colocation pin ignored)", r.Index, r.WorkerID)
		}
	}
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

// TestBootRegistersRuntimeEndpoint verifies that the runtime-returned endpoint
// URL is passed to the health-check, registered with the proxy, and carried on
// Result (EndpointURL, Tier, Provider, WorkerID).
func TestBootRegistersRuntimeEndpoint(t *testing.T) {
	var gotHealthURL string
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("ep-app")
	prx := proxy.New()

	p := deploy.Params{
		Slug:      "ep-app",
		BundleDir: t.TempDir(),
		Command:   []string{"sleep", "30"},
		Manager:   mgr,
		Proxy:     prx,
		HealthCheck: func(endpointURL string, _ time.Duration, _ http.RoundTripper) error {
			gotHealthURL = endpointURL
			return nil
		},
	}
	res, err := deploy.Run(p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Replicas) != 1 {
		t.Fatalf("got %d replicas; want 1", len(res.Replicas))
	}
	r := res.Replicas[0]

	// Health-check and Result must both carry the runtime endpoint URL.
	if r.EndpointURL == "" || gotHealthURL != r.EndpointURL {
		t.Fatalf("health-checked %q but Result.EndpointURL=%q", gotHealthURL, r.EndpointURL)
	}

	// Proxy must have been registered with the same URL (not a re-derived one).
	registeredURL := prx.ReplicaTargetURL("ep-app", r.Index)
	if registeredURL != r.EndpointURL {
		t.Fatalf("proxy registered %q but Result.EndpointURL=%q", registeredURL, r.EndpointURL)
	}

	// All four new Result fields must be populated.
	if r.Provider != "native" {
		t.Fatalf("want Provider=native, got %q", r.Provider)
	}
	if r.Tier != "local" {
		t.Fatalf("want Tier=local, got %q", r.Tier)
	}
	if r.WorkerID == "" {
		t.Fatal("WorkerID must be non-empty (native runtime stamps it with the PID)")
	}
}

// Auto-instrumentation must reach the command builder through every inferred-
// command Python boot, resolved as fleet default overridden by the manifest.
func TestRun_AutoInstrumentResolution(t *testing.T) {
	cases := []struct {
		name     string
		fleet    bool
		manifest string
		want     bool
	}{
		{"fleet default applies", true, "", true},
		{"manifest opts out of fleet default", true, "[tracing]\nauto = false\n", false},
		{"manifest opts in against fleet default", false, "[tracing]\nauto = true\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte("# app"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(tc.manifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			restoreSync := deploy.SetSyncHooksForTest(func(context.Context, string, []string) error { return nil }, func(context.Context, string, []string) error { return nil })
			defer restoreSync()

			var mu sync.Mutex
			var autos []bool
			restoreCmd := deploy.SetBuildCommandForTest(func(_ string, _, _ int, _ string, auto, _ bool) []string {
				mu.Lock()
				autos = append(autos, auto)
				mu.Unlock()
				return []string{"sleep", "30"}
			})
			defer restoreCmd()

			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			mgr.SetAutoInstrumentAppsDefault(tc.fleet)
			defer mgr.Stop("auto-res")
			prx := proxy.New()

			_, err := deploy.Run(deploy.Params{
				Slug: "auto-res", BundleDir: bundle,
				Manager: mgr, Proxy: prx,
				HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(autos) != 1 || autos[0] != tc.want {
				t.Errorf("builder calls = %v, want one call with %v", autos, tc.want)
			}
		})
	}
}

// A custom Params.Command bypasses command inference entirely; the wrapper
// must never be applied to user-supplied commands.
func TestRun_AutoInstrumentSkipsCustomCommand(t *testing.T) {
	bundle := t.TempDir()
	restoreCmd := deploy.SetBuildCommandForTest(func(string, int, int, string, bool, bool) []string {
		t.Error("buildCommand must not be called for a custom Command")
		return nil
	})
	defer restoreCmd()

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	defer mgr.Stop("auto-custom")
	prx := proxy.New()

	_, err := deploy.Run(deploy.Params{
		Slug: "auto-custom", BundleDir: bundle,
		Command: []string{"sleep", "30"},
		Manager: mgr, Proxy: prx,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

// recordingRuntime is a minimal Runtime that captures every StartParams it
// receives. It never spawns a real process, acting as a lightweight fake
// suitable for testing the command-substitution path without real OS overhead.
type recordingRuntime struct {
	mu     sync.Mutex
	starts []process.StartParams
}

func (r *recordingRuntime) record(p process.StartParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, p)
}

func (r *recordingRuntime) recorded() []process.StartParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]process.StartParams, len(r.starts))
	copy(out, r.starts)
	return out
}

func (r *recordingRuntime) HostPreparesDeps() bool    { return false }
func (r *recordingRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *recordingRuntime) HostProvidesAppData() bool { return false }
func (r *recordingRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.record(p)
	id := fmt.Sprintf("rec-%s-%d", p.Slug, p.Index)
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "recording",
		WorkerID: id,
		Handle:   process.RunHandle{ContainerID: id},
	}, nil
}
func (r *recordingRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (r *recordingRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (r *recordingRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (r *recordingRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

// TestRun_ManifestCommand_EachReplicaGetsOwnPort drives deploy.Run with two
// replicas and a manifest [app] command template containing {port} and {host}.
// It verifies:
//   - both replicas are booted with fully substituted commands (no {port}/{host})
//   - each replica gets the port that matches its StartParams.Port
//   - the two command slices carry distinct ports
//   - the two command slices do not share a backing array (the template is not mutated)
//   - reloading the manifest afterwards still yields the raw {port} template
func TestRun_ManifestCommand_EachReplicaGetsOwnPort(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(
		"[app]\ncommand = [\"serve\", \"--port\", \"{port}\", \"--host\", \"{host}\"]\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	defer mgr.Stop("cmd-tpl")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:      "cmd-tpl",
		BundleDir: bundle,
		Replicas:  2,
		Manager:   mgr,
		Proxy:     prx,
		// No Params.Command — the manifest [app] command is the template under test.
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if len(result.Replicas) != 2 {
		t.Fatalf("want 2 replicas, got %d", len(result.Replicas))
	}

	starts := rt.recorded()
	if len(starts) != 2 {
		t.Fatalf("want 2 recorded Start calls, got %d", len(starts))
	}

	// Index by replica index for deterministic assertions regardless of
	// goroutine scheduling order.
	byIndex := map[int]process.StartParams{}
	for _, s := range starts {
		byIndex[s.Index] = s
	}

	cmdA := byIndex[0].Command
	cmdB := byIndex[1].Command
	portA := byIndex[0].Port
	portB := byIndex[1].Port

	// Both commands must begin with "serve".
	if len(cmdA) == 0 || cmdA[0] != "serve" {
		t.Errorf("replica 0 command = %v; want first element \"serve\"", cmdA)
	}
	if len(cmdB) == 0 || cmdB[0] != "serve" {
		t.Errorf("replica 1 command = %v; want first element \"serve\"", cmdB)
	}

	// Helper: find the argument following --port.
	portArg := func(cmd []string) string {
		for i, arg := range cmd {
			if arg == "--port" && i+1 < len(cmd) {
				return cmd[i+1]
			}
		}
		return ""
	}

	portArgA := portArg(cmdA)
	portArgB := portArg(cmdB)

	// The --port argument must equal the recorded Port for each replica.
	if portArgA != fmt.Sprintf("%d", portA) {
		t.Errorf("replica 0: --port arg %q != StartParams.Port %d", portArgA, portA)
	}
	if portArgB != fmt.Sprintf("%d", portB) {
		t.Errorf("replica 1: --port arg %q != StartParams.Port %d", portArgB, portB)
	}

	// The two replicas must have different ports.
	if portA == portB {
		t.Errorf("replicas share the same port %d; each must get its own", portA)
	}

	// No unsubstituted placeholder must remain in either command.
	for _, tok := range []string{"{port}", "{host}"} {
		for i, cmd := range [][]string{cmdA, cmdB} {
			for _, arg := range cmd {
				if strings.Contains(arg, tok) {
					t.Errorf("replica %d command still contains placeholder %q: %v", i, tok, cmd)
				}
			}
		}
	}

	// The two command slices must not share a backing array: the template must
	// never be mutated in place. Comparing the address of the first element
	// catches a copy(dst, src) reuse — substituteCommand always allocates a
	// fresh make([]string, len(tpl)), so this must differ.
	if len(cmdA) > 0 && len(cmdB) > 0 &&
		fmt.Sprintf("%p", &cmdA[0]) == fmt.Sprintf("%p", &cmdB[0]) {
		t.Error("cmdA and cmdB share a backing array; the template was mutated or reused instead of copied per-replica")
	}

	// The on-disk manifest must still contain the raw {port} template — the
	// boot path must not overwrite the bundle file.
	m, err := deploy.LoadManifest(bundle)
	if err != nil {
		t.Fatalf("LoadManifest after deploy: %v", err)
	}
	if m == nil || len(m.App.Command) == 0 {
		t.Fatal("manifest is missing [app] command after deploy")
	}
	found := false
	for _, arg := range m.App.Command {
		if arg == "{port}" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("manifest [app] command no longer contains {port} after deploy: %v — template was modified on disk", m.App.Command)
	}
}

// A bad instrumentation overlay (dep conflict, broken instrumentor) is an
// observability regression, not an outage: the replica must come up
// uninstrumented after the instrumented attempt fails.
func TestRun_InstrumentedFailureFallsBackUninstrumented(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreSync := deploy.SetSyncHooksForTest(func(context.Context, string, []string) error { return nil }, func(context.Context, string, []string) error { return nil })
	defer restoreSync()

	var mu sync.Mutex
	var autos []bool
	restoreCmd := deploy.SetBuildCommandForTest(func(_ string, _, _ int, _ string, auto, _ bool) []string {
		mu.Lock()
		autos = append(autos, auto)
		mu.Unlock()
		return []string{"sleep", "30"}
	})
	defer restoreCmd()

	// The instrumented attempt fails its health check; the fallback passes.
	var calls atomic.Int32
	hc := func(string, time.Duration, http.RoundTripper) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("simulated instrumented-launch failure")
		}
		return nil
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	defer mgr.Stop("auto-fallback")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug: "auto-fallback", BundleDir: bundle,
		Manager: mgr, Proxy: prx,
		HealthCheck: hc,
	})
	if err != nil {
		t.Fatalf("run should succeed via uninstrumented fallback: %v", err)
	}
	if len(result.Replicas) != 1 {
		t.Fatalf("want 1 replica, got %d", len(result.Replicas))
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []bool{true, false}; !reflect.DeepEqual(autos, want) {
		t.Errorf("builder calls = %v, want %v (instrumented then fallback)", autos, want)
	}
}

func TestResolveWorkerIsolation(t *testing.T) {
	cases := []struct {
		perApp string
		def    string
		want   string
	}{
		// per-app value wins when set
		{"per_session", "multiplex", "per_session"},
		{"grouped", "multiplex", "grouped"},
		{"multiplex", "grouped", "multiplex"},
		// fall through to fleet default when per-app is empty
		{"", "grouped", "grouped"},
		{"", "per_session", "per_session"},
		{"", "multiplex", "multiplex"},
		// both empty - hard default is multiplex
		{"", "", "multiplex"},
	}
	for _, c := range cases {
		got := deploy.ResolveWorkerIsolation(c.perApp, c.def)
		if got != c.want {
			t.Errorf("ResolveWorkerIsolation(%q, %q) = %q; want %q", c.perApp, c.def, got, c.want)
		}
	}
}
