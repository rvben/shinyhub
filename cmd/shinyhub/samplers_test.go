package main

import (
	"os"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
)

// tierRuntime is a distinguishable stand-in for a tier's runtime.
type tierRuntime struct {
	process.Runtime
	name string
}

func samplerManager(t *testing.T, tiers map[string]process.Runtime) *process.Manager {
	t.Helper()
	mgr := process.NewManager(t.TempDir(), &tierRuntime{name: "fallback"})
	for name, rt := range tiers {
		mgr.RegisterRuntime(name, rt)
	}
	return mgr
}

func assertRuntimeSampler(t *testing.T, role string, got process.Sampler, want process.Runtime) {
	t.Helper()
	rs, ok := got.(*process.RuntimeSampler)
	if !ok {
		t.Fatalf("%s sampler = %T, want *process.RuntimeSampler", role, got)
	}
	if rs.Runtime != want {
		t.Fatalf("%s sampler reads runtime %v, want the default tier's %v", role, rs.Runtime, want)
	}
}

// A config that declares a docker tier without the legacy runtime.mode field
// still samples through the container runtime: the default tier's resolved
// runtime decides, not cfg.Runtime.Mode.
func TestReplicaSamplersFollowTheDefaultTierRuntime(t *testing.T) {
	docker := &tierRuntime{name: "docker"}
	mgr := samplerManager(t, map[string]process.Runtime{"containers": docker, "gpu": &tierRuntime{name: "gpu"}})
	cfg := &config.Config{Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
		{Name: "containers", Runtime: "docker"},
		{Name: "gpu", Runtime: "native"},
	}}}

	apiSampler, historySampler := replicaSamplers(cfg, mgr)
	assertRuntimeSampler(t, "api", apiSampler, docker)
	assertRuntimeSampler(t, "history", historySampler, docker)
	if apiSampler == historySampler {
		t.Fatal("api and history share one sampler; their CPU deltas would interfere")
	}
}

func TestReplicaSamplersHonorTheLegacyDockerMode(t *testing.T) {
	docker := &tierRuntime{name: "docker"}
	mgr := samplerManager(t, map[string]process.Runtime{"local": docker})
	cfg := &config.Config{Runtime: config.RuntimeConfig{Mode: "docker"}}

	apiSampler, historySampler := replicaSamplers(cfg, mgr)
	assertRuntimeSampler(t, "api", apiSampler, docker)
	assertRuntimeSampler(t, "history", historySampler, docker)
}

// A host-process default tier samples host PIDs. The API sampler tolerates
// PID-less replicas; the history collector keeps its own sampler so its CPU
// deltas are independent of on-demand API reads.
func TestReplicaSamplersUseHostPIDsForANativeDefaultTier(t *testing.T) {
	mgr := samplerManager(t, map[string]process.Runtime{"local": &tierRuntime{name: "native"}})
	cfg := &config.Config{Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
		{Name: "local", Runtime: "native"},
		{Name: "containers", Runtime: "docker"},
	}}}

	apiSampler, historySampler := replicaSamplers(cfg, mgr)
	if _, ok := apiSampler.(*hostSampler); !ok {
		t.Fatalf("api sampler = %T, want *hostSampler", apiSampler)
	}
	if _, ok := historySampler.(*process.GopsutilSampler); !ok {
		t.Fatalf("history sampler = %T, want *process.GopsutilSampler", historySampler)
	}
}

// A container or remote replica has no host PID. The status endpoint reads a
// sampler error as a dead replica, so the API sampler reports it as unmeasured
// rather than probing PID 0, while a host process is still measured.
func TestHostSamplerReportsPIDlessReplicasAsUnmeasured(t *testing.T) {
	var s hostSampler
	stats, err := s.Sample(process.RunHandle{ContainerID: "c0ffee"})
	if err != nil {
		t.Fatalf("PID-less replica: err = %v, want nil", err)
	}
	if stats.CPUPercent != nil || stats.RSSBytes != 0 {
		t.Fatalf("PID-less replica: stats = %+v, want unmeasured", stats)
	}

	stats, err = s.Sample(process.RunHandle{PID: os.Getpid()})
	if err != nil {
		t.Fatalf("host process: %v", err)
	}
	if stats.RSSBytes <= 0 {
		t.Fatalf("host process RSS = %d, want a measured value", stats.RSSBytes)
	}
}
