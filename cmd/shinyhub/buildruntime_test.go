package main

import (
	"context"
	"testing"

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
