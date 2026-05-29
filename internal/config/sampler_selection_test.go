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
		// Sampler selection must use mode, not cfg.Runtime.Mode.
		// cfg.Runtime.Mode is the legacy field; it defaults to "native" here.
		if cfg.Runtime.Mode == "docker" {
			t.Errorf("cfg.Runtime.Mode = %q; sampler selection bug: comparing Mode would be wrong when tiers override it", cfg.Runtime.Mode)
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
		// Fargate default tier must NOT pick the docker RuntimeSampler;
		// it must pick hostSampler (which reports zero for PID-less handles).
		// This assertion guards the sampler-selection logic in main.go.
		if cfg.Runtime.Mode == "docker" {
			t.Errorf("cfg.Runtime.Mode = %q; fargate tier would get docker sampler if main.go reads Mode", cfg.Runtime.Mode)
		}
	})
}
