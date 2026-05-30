package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestSamplerSelectionUsesResolvedMode asserts that when the default tier's
// runtime is "docker" (declared via tiers[], regardless of runtime.mode),
// RuntimeForTier returns "docker" for the default tier. This is the value
// cmd/shinyhub/main.go stores in defaultMode and must use for sampler
// selection - NOT cfg.Runtime.Mode.
func TestSamplerSelectionUsesResolvedMode(t *testing.T) {
	t.Run("tiers declares docker, mode is empty", func(t *testing.T) {
		// Synthesized-tier path: no explicit runtime.mode, but tiers[0].runtime=docker.
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: gpu
      runtime: docker
  docker:
    socket: /var/run/docker.sock
`)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		defaultTier := cfg.Runtime.DefaultTierName()
		if defaultTier != "gpu" {
			t.Fatalf("DefaultTierName = %q, want gpu", defaultTier)
		}
		mode, ok := cfg.Runtime.RuntimeForTier(defaultTier)
		if !ok {
			t.Fatalf("RuntimeForTier(%q): not found", defaultTier)
		}
		if mode != "docker" {
			t.Errorf("RuntimeForTier(%q) = %q, want docker", defaultTier, mode)
		}
		// The two fields must diverge: RuntimeForTier returns "docker" while
		// cfg.Runtime.Mode does not. This is exactly the condition that proves
		// reading the legacy Mode field would select the wrong sampler for this
		// config.
		if cfg.Runtime.Mode == mode {
			t.Errorf("cfg.Runtime.Mode = %q equals the resolved mode; fields no longer diverge, so the test no longer guards the sampler-selection bug", cfg.Runtime.Mode)
		}
	})

	t.Run("tiers declares fargate, mode is native", func(t *testing.T) {
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: tasks
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-app:1
    container_name: app
    subnets:
      - subnet-aaa
    task_cpu_units: 256
    task_memory_mb: 512
    control_plane_url: "https://cp.example.com"
`)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		mode, ok := cfg.Runtime.RuntimeForTier(cfg.Runtime.DefaultTierName())
		if !ok {
			t.Fatal("RuntimeForTier: not found")
		}
		if mode != "fargate" {
			t.Errorf("mode = %q, want fargate", mode)
		}
		// cfg.Runtime.Mode must not equal the resolved mode: the legacy field
		// does not reflect the tier's runtime, so sampler selection must read
		// the resolved mode, not cfg.Runtime.Mode.
		if cfg.Runtime.Mode == mode {
			t.Errorf("cfg.Runtime.Mode = %q equals the resolved mode; fields no longer diverge, so the test no longer guards the sampler-selection bug", cfg.Runtime.Mode)
		}
	})
}
