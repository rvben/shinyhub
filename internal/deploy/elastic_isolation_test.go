package deploy_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestRun_PerSession_NoReplicasBooted verifies that deploy.Run with
// WorkerIsolation="per_session" returns immediately with an empty PoolResult
// (no fixed replicas booted) and does not spawn any OS processes. The proxy
// pool is set up in elastic mode; workers are spawned on demand at request time.
func TestRun_PerSession_NoReplicasBooted(t *testing.T) {
	// A real bundle: the deploy prepares it (type detection, dependency build)
	// before handing routing to the on-demand spawner, so an empty directory
	// would legitimately fail here rather than exercising the boot skip.
	bundle := writeElasticBundle(t, "")
	defer stubBundleBuild(t)()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("elastic-ps")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:             "elastic-ps",
		BundleDir:        bundle,
		Replicas:         2, // would boot 2 for multiplex; elastic ignores this
		Manager:          mgr,
		Proxy:            prx,
		WorkerIsolation:  "per_session",
		WorkerMaxWorkers: 5,
		// HealthCheck and Command are intentionally absent: elastic deploy
		// must not attempt to boot or health-check any replica.
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Replicas) != 0 {
		t.Errorf("elastic deploy booted %d replicas, want 0", len(result.Replicas))
	}

	// No OS processes must have been spawned for this slug.
	for _, pi := range mgr.All() {
		if pi.Slug == "elastic-ps" {
			t.Errorf("elastic deploy started a process (PID %d) for slug %q; want none", pi.PID, pi.Slug)
		}
	}

	// The proxy pool must NOT have a fixed replica (elastic workers spawn on demand).
	if prx.HasLiveReplica("elastic-ps") {
		t.Error("elastic deploy registered a fixed replica in the proxy pool; want none")
	}

	// The elastic worker count must be zero (no workers pre-spawned).
	if n := prx.ElasticWorkerCount("elastic-ps"); n != 0 {
		t.Errorf("elastic worker count = %d, want 0 (workers are demand-driven)", n)
	}
}

// TestRun_Grouped_NoReplicasBooted verifies the same "skip boot loop" behavior
// for WorkerIsolation="grouped".
func TestRun_Grouped_NoReplicasBooted(t *testing.T) {
	bundle := writeElasticBundle(t, "")
	defer stubBundleBuild(t)()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("elastic-gr")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:              "elastic-gr",
		BundleDir:         bundle,
		Replicas:          1,
		Manager:           mgr,
		Proxy:             prx,
		WorkerIsolation:   "grouped",
		WorkerGroupedSize: 3,
		WorkerMaxWorkers:  10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Replicas) != 0 {
		t.Errorf("grouped deploy booted %d replicas, want 0", len(result.Replicas))
	}
	if prx.HasLiveReplica("elastic-gr") {
		t.Error("grouped deploy registered a fixed replica; want none")
	}
}

// TestRun_Multiplex_StillBootsReplicas is a regression test verifying that the
// elastic early-return does NOT affect multiplex apps: they still spawn fixed
// replicas and register them with the proxy.
func TestRun_Multiplex_StillBootsReplicas(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("multiplex-reg")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:            "multiplex-reg",
		BundleDir:       bundle,
		Replicas:        2,
		Manager:         mgr,
		Proxy:           prx,
		WorkerIsolation: "multiplex",
		Command:         []string{"sleep", "30"},
		HealthCheck:     func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Replicas) != 2 {
		t.Errorf("multiplex deploy got %d replicas, want 2", len(result.Replicas))
	}
	if !prx.HasLiveReplica("multiplex-reg") {
		t.Error("multiplex deploy must register replicas with the proxy")
	}
}

// TestRun_DefaultIsolation_ActsAsMultiplex verifies that an empty
// WorkerIsolation (inherit default) with an empty DefaultWorkerIsolation
// resolves to multiplex behavior and boots fixed replicas.
func TestRun_DefaultIsolation_ActsAsMultiplex(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.Stop("default-iso")
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:      "default-iso",
		BundleDir: bundle,
		Replicas:  1,
		Manager:   mgr,
		Proxy:     prx,
		// WorkerIsolation and DefaultWorkerIsolation both empty -> multiplex
		Command:     []string{"sleep", "30"},
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Replicas) != 1 {
		t.Errorf("default isolation got %d replicas, want 1", len(result.Replicas))
	}
}
