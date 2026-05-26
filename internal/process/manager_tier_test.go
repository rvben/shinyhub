package process_test

import (
	"context"
	"io"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// stubRuntime is a minimal Runtime double whose capability answers
// (HostPreparesDeps, AppBindHost) are configurable per instance, so a test can
// register distinct runtimes under distinct tiers and assert that the Manager
// routes capability queries to the right tier's runtime.
type stubRuntime struct {
	hostDeps bool
	bindHost string
}

func (s stubRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, nil
}
func (s stubRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (s stubRuntime) Wait(context.Context, process.RunHandle) error  { return nil }
func (s stubRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (s stubRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (s stubRuntime) HostPreparesDeps() bool    { return s.hostDeps }
func (s stubRuntime) AppBindHost() string       { return s.bindHost }
func (s stubRuntime) HostProvidesAppData() bool { return true }

func TestHostPreparesDepsFor_RoutesByTier(t *testing.T) {
	m := process.NewManager(t.TempDir(), stubRuntime{hostDeps: true, bindHost: "127.0.0.1"})
	m.RegisterRuntime("burst", stubRuntime{hostDeps: false, bindHost: "0.0.0.0"})

	if got := m.HostPreparesDepsFor(process.DefaultTier); !got {
		t.Errorf("default tier HostPreparesDepsFor = %v, want true", got)
	}
	if got := m.HostPreparesDepsFor("burst"); got {
		t.Errorf("burst tier HostPreparesDepsFor = %v, want false", got)
	}
}

func TestAppBindHostFor_RoutesByTier(t *testing.T) {
	m := process.NewManager(t.TempDir(), stubRuntime{hostDeps: true, bindHost: "127.0.0.1"})
	m.RegisterRuntime("burst", stubRuntime{hostDeps: false, bindHost: "0.0.0.0"})

	if got := m.AppBindHostFor(process.DefaultTier); got != "127.0.0.1" {
		t.Errorf("default tier AppBindHostFor = %q, want 127.0.0.1", got)
	}
	if got := m.AppBindHostFor("burst"); got != "0.0.0.0" {
		t.Errorf("burst tier AppBindHostFor = %q, want 0.0.0.0", got)
	}
}

func TestSetDefaultTier_RekeysDefaultRuntime(t *testing.T) {
	// NewManager registers the seed runtime under DefaultTier ("local").
	// SetDefaultTier renames that tier so a config whose first tier is not
	// "local" still resolves empty/unknown tiers to the seed runtime.
	m := process.NewManager(t.TempDir(), stubRuntime{hostDeps: true, bindHost: "127.0.0.1"})
	m.SetDefaultTier("primary")

	// The seed runtime now answers under its new name.
	if got := m.AppBindHostFor("primary"); got != "127.0.0.1" {
		t.Errorf("renamed default tier AppBindHostFor = %q, want 127.0.0.1", got)
	}
	// An unknown tier falls back to the renamed default, not the old "local".
	if got := m.AppBindHostFor("nonexistent"); got != "127.0.0.1" {
		t.Errorf("unknown tier after rename AppBindHostFor = %q, want 127.0.0.1", got)
	}
	// An empty/zero-value tier resolves through the renamed default too.
	if got := m.HostPreparesDepsFor(""); !got {
		t.Errorf("empty tier after rename HostPreparesDepsFor = %v, want true", got)
	}
}

func TestSetDefaultTier_NoopOnEmptyOrSameName(t *testing.T) {
	m := process.NewManager(t.TempDir(), stubRuntime{hostDeps: true, bindHost: "127.0.0.1"})
	m.SetDefaultTier("")                  // empty: no change
	m.SetDefaultTier(process.DefaultTier) // same name: no change

	if got := m.AppBindHostFor(process.DefaultTier); got != "127.0.0.1" {
		t.Errorf("default tier AppBindHostFor = %q, want 127.0.0.1", got)
	}
}

func TestHostPreparesDepsFor_UnknownTierFallsBackToDefault(t *testing.T) {
	m := process.NewManager(t.TempDir(), stubRuntime{hostDeps: true, bindHost: "127.0.0.1"})

	if got := m.HostPreparesDepsFor("nonexistent"); !got {
		t.Errorf("unknown tier HostPreparesDepsFor = %v, want default true", got)
	}
	if got := m.AppBindHostFor("nonexistent"); got != "127.0.0.1" {
		t.Errorf("unknown tier AppBindHostFor = %q, want default 127.0.0.1", got)
	}
}
