package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
)

// TestBuildRuntime_NativeIsSnapshotter asserts the native tier's runtime is
// wired as a Snapshotter so warm-wake (SIGSTOP + per-app cgroup reclaim) is
// reachable through buildRuntime. The actual freeze/reclaim mechanism is
// linux/moxie-verified; this only pins the wiring so a regression that returns a
// non-snapshot-capable native runtime fails the build.
func TestBuildRuntime_NativeIsSnapshotter(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_SNAPSHOT_ENABLED", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt, err := buildRuntime(context.Background(), config.TierConfig{Name: "local", Runtime: "native"}, cfg, nil)
	if err != nil {
		t.Fatalf("buildRuntime native: %v", err)
	}
	if _, ok := rt.(process.Snapshotter); !ok {
		t.Fatalf("native runtime %T does not implement process.Snapshotter; warm-wake not wired", rt)
	}
}

func TestBuildRuntimeScalewayServerlessRequiresCredentials(t *testing.T) {
	t.Setenv("SCW_ACCESS_KEY", "")
	t.Setenv("SCW_SECRET_KEY", "")
	cfg := validScalewayRuntimeConfig()

	_, err := buildRuntime(context.Background(), config.TierConfig{Name: "serverless", Runtime: "scaleway_serverless"}, cfg, []byte("bundle-key"))
	if err == nil || !strings.Contains(err.Error(), "SCW_ACCESS_KEY and SCW_SECRET_KEY") {
		t.Fatalf("buildRuntime error = %v", err)
	}
}

func TestBuildRuntimeScalewayServerlessImplementsManagedContract(t *testing.T) {
	t.Setenv("SCW_ACCESS_KEY", "SCWABCDEFGHIJKLMNOPQ")
	t.Setenv("SCW_SECRET_KEY", "00000000-0000-4000-8000-000000000000")
	cfg := validScalewayRuntimeConfig()

	rt, err := buildRuntime(context.Background(), config.TierConfig{Name: "serverless", Runtime: "scaleway_serverless"}, cfg, []byte("bundle-key"))
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	if _, ok := rt.(process.ManagedRuntime); !ok {
		t.Fatalf("scaleway runtime %T does not implement process.ManagedRuntime", rt)
	}
}

func validScalewayRuntimeConfig() *config.Config {
	return &config.Config{Runtime: config.RuntimeConfig{Scaleway: config.ScalewayRuntimeConfig{
		Region:          "nl-ams",
		ProjectID:       "00000000-0000-4000-8000-000000000001",
		NamespaceID:     "00000000-0000-4000-8000-000000000002",
		Image:           "rg.nl-ams.scw.cloud/shinyhub/runner:latest",
		ControlPlaneURL: "https://demo.shinyhub.dev",
		BundleTokenTTL:  10 * time.Minute,
	}}}
}
