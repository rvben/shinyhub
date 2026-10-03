package process_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// perPIDRuntime tracks each process separately, so one generation can ignore
// signals while another keeps running untouched. Wait blocks until the process
// is gone or its context ends.
type perPIDRuntime struct {
	mu         sync.Mutex
	nextPID    int
	gone       map[int]bool
	ignoreTerm map[int]bool
	signals    map[int][]syscall.Signal
	// signalDelay makes every Signal call block this long first, modelling a
	// slow or hung remote runtime.
	signalDelay time.Duration
	// ignoreNext makes the next started process ignore every signal.
	ignoreNext bool
}

func newPerPIDRuntime() *perPIDRuntime {
	return &perPIDRuntime{nextPID: 80000, gone: map[int]bool{}, ignoreTerm: map[int]bool{}, signals: map[int][]syscall.Signal{}}
}

func (r *perPIDRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	pid := r.nextPID
	r.ignoreTerm[pid] = r.ignoreNext
	r.ignoreNext = false
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *perPIDRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	r.mu.Lock()
	delay := r.signalDelay
	r.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone[h.PID] {
		return syscall.ESRCH
	}
	r.signals[h.PID] = append(r.signals[h.PID], sig)
	if !r.ignoreTerm[h.PID] {
		r.gone[h.PID] = true
	}
	return nil
}

func (r *perPIDRuntime) exit(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gone[pid] = true
}

func (r *perPIDRuntime) signalCount(pid int) int {
	return len(r.signalsTo(pid))
}

func (r *perPIDRuntime) signalsTo(pid int) []syscall.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]syscall.Signal(nil), r.signals[pid]...)
}

func (r *perPIDRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	for {
		r.mu.Lock()
		gone := r.gone[h.PID]
		r.mu.Unlock()
		if gone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (r *perPIDRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *perPIDRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *perPIDRuntime) HostPreparesDeps() bool    { return false }
func (r *perPIDRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *perPIDRuntime) HostProvidesAppData() bool { return false }

// fencedPreviousGeneration starts deployment 101 with a process that ignores
// signals, leaves its slot fenced and claimed after an unconfirmed stop, then
// activates deployment 202 beside it. It returns the fenced incarnation and
// both PIDs.
func fencedPreviousGeneration(t *testing.T, rt *perPIDRuntime) (mgr *process.Manager, gen uint64, oldPID, newPID int) {
	t.Helper()
	mgr = process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(30 * time.Millisecond)

	rt.mu.Lock()
	rt.ignoreNext = true
	rt.mu.Unlock()
	old, err := mgr.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"app"}, DeploymentID: 101})
	if err != nil {
		t.Fatalf("start previous generation: %v", err)
	}
	gen, _, ok := mgr.ReplicaIncarnation("demo", 0)
	if !ok {
		t.Fatal("previous generation has no incarnation")
	}
	if err := mgr.StopReplicaIncarnation("demo", 0, gen); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("first stop = %v, want ErrStopUnconfirmed", err)
	}
	if !mgr.ClaimStopPending("demo", 0, gen) {
		t.Fatal("claim of the fenced previous generation failed")
	}

	next, err := mgr.Start(process.StartParams{
		Slug: "demo", Index: 0, Port: 20002, Command: []string{"app"}, DeploymentID: 202, GenerationScoped: true,
	})
	if err != nil {
		t.Fatalf("start next generation: %v", err)
	}
	if _, err := mgr.ActivateGeneration("demo", 202); err != nil {
		t.Fatalf("activate next generation: %v", err)
	}
	return mgr, gen, old.PID, next.PID
}

// TestStopReplicaIncarnation_FindsFencedPreviousGeneration covers a stop retry
// queued for a generation that a later activation has moved out of the active
// pool. The incarnation is still tracked and its process may still be alive,
// so the retry must keep targeting it rather than report it gone, and must
// never touch the newly active generation in the same slot index.
func TestStopReplicaIncarnation_FindsFencedPreviousGeneration(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, oldPID, newPID := fencedPreviousGeneration(t, rt)

	if err := mgr.StopReplicaIncarnation("demo", 0, gen); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("retry against the live previous generation = %v, want ErrStopUnconfirmed", err)
	}

	// Once the process exits, a retry proves it. The retry queue retries on
	// any error but ErrIncarnationGone, so loop the same way: a signal can
	// race the exit monitor and fail before the monitor records the exit.
	rt.exit(oldPID)
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := mgr.StopReplicaIncarnation("demo", 0, gen)
		if err == nil {
			break
		}
		if errors.Is(err, process.ErrIncarnationGone) || time.Now().After(deadline) {
			t.Fatalf("retry once the previous generation exited = %v, want nil", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The claimed fence stays until the owning queue releases it.
	if _, ok := mgr.GetGenerationReplica("demo", 101, 0); !ok {
		t.Fatal("claimed fence of the previous generation dropped before its release")
	}
	mgr.ReleaseStopPending("demo", 0, gen)
	if _, ok := mgr.GetGenerationReplica("demo", 101, 0); ok {
		t.Error("previous generation still tracked after its confirmed stop and release")
	}
	if got, ok := mgr.GetReplica("demo", 0); !ok || got.PID != newPID {
		t.Fatalf("active replica = %+v, %v; want the next generation pid %d untouched", got, ok, newPID)
	}
	if n := rt.signalCount(newPID); n != 0 {
		t.Errorf("the active generation was signalled %d times by a retry for the previous one", n)
	}
}

// TestReleaseStopPending_ReleasesFencedPreviousGeneration covers the owning
// queue releasing its claimed fence after an activation: the release must
// find the entry in the previous generation's pool, or the claimed fence
// outlives the process forever.
func TestReleaseStopPending_ReleasesFencedPreviousGeneration(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, oldPID, newPID := fencedPreviousGeneration(t, rt)

	rt.exit(oldPID)
	mgr.ReleaseStopPending("demo", 0, gen)

	if _, ok := mgr.GetGenerationReplica("demo", 101, 0); ok {
		t.Error("claimed fence of the previous generation survived its release")
	}
	if got, ok := mgr.GetReplica("demo", 0); !ok || got.PID != newPID {
		t.Fatalf("release disturbed the active generation: %+v, %v", got, ok)
	}
}
