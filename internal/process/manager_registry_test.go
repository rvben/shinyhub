package process

import (
	"context"
	"io"
	"syscall"
	"testing"
)

// stubTierRuntime is a minimal Runtime used to exercise the tier registry.
type stubTierRuntime struct{}

func (stubTierRuntime) Start(context.Context, StartParams, io.Writer) (ReplicaEndpoint, error) {
	return ReplicaEndpoint{}, nil
}
func (stubTierRuntime) Signal(RunHandle, syscall.Signal) error { return nil }
func (stubTierRuntime) Wait(context.Context, RunHandle) error  { return nil }
func (stubTierRuntime) Stats(context.Context, RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (stubTierRuntime) RunOnce(context.Context, StartParams, io.Writer) (ExitInfo, error) {
	return ExitInfo{}, nil
}
func (stubTierRuntime) HostPreparesDeps() bool    { return false }
func (stubTierRuntime) AppBindHost() string       { return "0.0.0.0" }
func (stubTierRuntime) HostProvidesAppData() bool { return false }

func TestManager_RegisterAndRemoveRuntime_Concurrent(t *testing.T) {
	m := NewManager(t.TempDir(), &stubTierRuntime{})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			m.RegisterRuntime("remote", &stubTierRuntime{})
			m.removeRuntime("remote")
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = m.runtimeFor("remote")
	}
	<-done
}

func TestManager_RemoveRuntime_FallsBackToDefault(t *testing.T) {
	def := &stubTierRuntime{}
	m := NewManager(t.TempDir(), def)
	other := &stubTierRuntime{}
	m.RegisterRuntime("remote", other)
	if m.runtimeFor("remote") != other {
		t.Fatal("expected registered runtime for tier remote")
	}
	m.removeRuntime("remote")
	if m.runtimeFor("remote") != def {
		t.Error("after removeRuntime, runtimeFor should fall back to the default runtime")
	}
}
