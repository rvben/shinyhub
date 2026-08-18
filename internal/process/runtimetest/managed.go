// Package runtimetest provides behavioral conformance tests for process
// runtimes. Provider packages supply a stateful fake at their external API
// boundary; this package exercises only the public process.ManagedRuntime
// contract, so implementations can change without rewriting the specification.
package runtimetest

import (
	"context"
	"io"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// Factory returns a fresh managed runtime and one valid replica request.
type Factory func(t *testing.T) (process.ManagedRuntime, process.StartParams)

// ManagedRuntime verifies the lifecycle every remote managed provider must
// expose to ShinyHub: wake, recoverable inventory, sleep, wake again, and
// idempotent app cleanup.
func ManagedRuntime(t *testing.T, provider, workerID string, factory Factory) {
	t.Helper()
	rt, params := factory(t)

	first := start(t, rt, params, provider, workerID)
	assertInventory(t, rt, params, true)
	stop(t, rt, first.Handle)
	assertInventory(t, rt, params, false)

	second := start(t, rt, params, provider, workerID)
	assertInventory(t, rt, params, true)
	stop(t, rt, second.Handle)

	if err := rt.CleanupApp(context.Background(), params.AppID); err != nil {
		t.Fatalf("CleanupApp: %v", err)
	}
	if err := rt.CleanupApp(context.Background(), params.AppID); err != nil {
		t.Fatalf("CleanupApp must be idempotent: %v", err)
	}
	assertInventoryAbsent(t, rt, params)
}

func start(t *testing.T, rt process.ManagedRuntime, params process.StartParams, provider, workerID string) process.ReplicaEndpoint {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ep, err := rt.Start(ctx, params, io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.URL == "" {
		t.Fatal("Start returned an empty route URL")
	}
	if ep.Handle.ContainerID == "" {
		t.Fatal("Start returned an empty managed-resource handle")
	}
	if ep.Provider != provider {
		t.Fatalf("provider = %q, want %q", ep.Provider, provider)
	}
	if ep.WorkerID != workerID {
		t.Fatalf("worker ID = %q, want %q", ep.WorkerID, workerID)
	}
	return ep
}

func stop(t *testing.T, rt process.ManagedRuntime, handle process.RunHandle) {
	t.Helper()
	if err := rt.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rt.Wait(ctx, handle); err != nil {
		t.Fatalf("Wait after Signal: %v", err)
	}
}

func assertInventory(t *testing.T, rt process.ManagedRuntime, params process.StartParams, wantRunning bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	items, err := rt.Inventory(ctx)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	foundRunning := false
	for _, item := range items {
		if item.Labels[process.LabelSlug] != params.Slug ||
			item.Labels[process.LabelReplicaIndex] != strconv.Itoa(params.Index) ||
			item.Labels[process.LabelDeploymentID] != strconv.FormatInt(params.DeploymentID, 10) {
			continue
		}
		if item.Running && item.URL != "" {
			foundRunning = true
		}
	}
	if foundRunning != wantRunning {
		t.Fatalf("inventory running+routable = %v, want %v; items=%+v", foundRunning, wantRunning, items)
	}
}

func assertInventoryAbsent(t *testing.T, rt process.ManagedRuntime, params process.StartParams) {
	t.Helper()
	items, err := rt.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory after CleanupApp: %v", err)
	}
	for _, item := range items {
		if item.Labels["shinyhub.app_id"] == strconv.FormatInt(params.AppID, 10) {
			t.Fatalf("CleanupApp retained app resource: %+v", item)
		}
	}
}
