package api

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// fakeFirstFireStore scripts LastSuccessfulRun outcomes per call: a nil entry
// means "found a successful run" (closed gate), a non-nil entry is returned as
// the gate error. LatestRegisterRunID is scripted the same way via markerIDs
// and markerErrs (a nil/absent markerErrs entry means the query succeeds). The
// last entry of each script repeats for extra calls.
type fakeFirstFireStore struct {
	calls       int
	outs        []error
	markerCalls int
	markerIDs   []int64
	markerErrs  []error
}

func (f *fakeFirstFireStore) LastSuccessfulRun(int64) (*db.ScheduleRun, error) {
	i := f.calls
	f.calls++
	if i >= len(f.outs) {
		i = len(f.outs) - 1
	}
	if f.outs[i] == nil {
		return &db.ScheduleRun{ID: 99, Status: "succeeded"}, nil
	}
	return nil, f.outs[i]
}

func (f *fakeFirstFireStore) LatestRegisterRunID(int64) (int64, error) {
	i := f.markerCalls
	f.markerCalls++
	if len(f.markerErrs) > 0 {
		j := i
		if j >= len(f.markerErrs) {
			j = len(f.markerErrs) - 1
		}
		if f.markerErrs[j] != nil {
			return 0, f.markerErrs[j]
		}
	}
	if len(f.markerIDs) == 0 {
		return 0, nil
	}
	if i >= len(f.markerIDs) {
		i = len(f.markerIDs) - 1
	}
	return f.markerIDs[i], nil
}

// fakeFirstFireRunner scripts Run outcomes per call the same way; a nil entry
// dispatches successfully.
type fakeFirstFireRunner struct {
	calls int
	outs  []error
}

func (f *fakeFirstFireRunner) Run(int64, string, *int64) (int64, error) {
	i := f.calls
	f.calls++
	if i >= len(f.outs) {
		i = len(f.outs) - 1
	}
	if f.outs[i] != nil {
		return 0, f.outs[i]
	}
	return int64(41 + f.calls), nil
}

var errTransient = errors.New("driver: bad connection")

// A single transient gate error must not lose the first-fire: a swallowed
// pre-dispatch failure leaves no run row, so the scheduler's interrupted-run
// reconcile has nothing to retry and the run would silently never happen.
func TestFirstFire_TransientGateErrorRetriesOnce(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{errTransient, db.ErrNotFound}}
	runner := &fakeFirstFireRunner{outs: []error{nil}}
	id, err := firstFire(store, runner, 1, 0)
	if err != nil {
		t.Fatalf("firstFire error = %v, want nil (transient gate error must be retried)", err)
	}
	if id == 0 {
		t.Fatal("firstFire returned id 0, want a dispatched run id")
	}
	if runner.calls != 1 {
		t.Errorf("runner.calls = %d, want 1", runner.calls)
	}
}

// A single transient dispatch error is retried the same way.
func TestFirstFire_TransientDispatchErrorRetriesOnce(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{db.ErrNotFound}}
	runner := &fakeFirstFireRunner{outs: []error{errTransient, nil}}
	id, err := firstFire(store, runner, 1, 0)
	if err != nil {
		t.Fatalf("firstFire error = %v, want nil (transient dispatch error must be retried)", err)
	}
	if id == 0 {
		t.Fatal("firstFire returned id 0, want a dispatched run id")
	}
	if runner.calls != 2 {
		t.Errorf("runner.calls = %d, want 2", runner.calls)
	}
}

// A persistent error gives up after exactly two attempts: first-fire stays
// best-effort and must not turn into an unbounded retry loop on the create
// request path.
func TestFirstFire_PersistentErrorGivesUpAfterTwoAttempts(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{errTransient}}
	runner := &fakeFirstFireRunner{outs: []error{nil}}
	_, err := firstFire(store, runner, 1, 0)
	if err == nil {
		t.Fatal("firstFire error = nil, want the persistent gate error")
	}
	if store.calls != 2 {
		t.Errorf("store.calls = %d, want 2 (one retry, then give up)", store.calls)
	}
	if runner.calls != 0 {
		t.Errorf("runner.calls = %d, want 0 (never dispatch on uncertain gate state)", runner.calls)
	}
}

// A closed gate (prior successful run) skips without dispatching or retrying.
func TestFirstFire_ClosedGateSkipsWithoutDispatch(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{nil}}
	runner := &fakeFirstFireRunner{outs: []error{nil}}
	_, err := firstFire(store, runner, 1, 0)
	if !errors.Is(err, errFirstFireAlreadySucceeded) {
		t.Fatalf("firstFire error = %v, want errFirstFireAlreadySucceeded", err)
	}
	if store.calls != 1 {
		t.Errorf("store.calls = %d, want 1 (closed gate is not retried)", store.calls)
	}
	if runner.calls != 0 {
		t.Errorf("runner.calls = %d, want 0", runner.calls)
	}
}

// The retry re-checks the gate, so a run that succeeded between the failed
// dispatch and the retry is never double-fired.
func TestFirstFire_RetryRechecksGate(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{db.ErrNotFound, nil}}
	runner := &fakeFirstFireRunner{outs: []error{errTransient}}
	_, err := firstFire(store, runner, 1, 0)
	if !errors.Is(err, errFirstFireAlreadySucceeded) {
		t.Fatalf("firstFire error = %v, want errFirstFireAlreadySucceeded from the re-checked gate", err)
	}
	if runner.calls != 1 {
		t.Errorf("runner.calls = %d, want 1 (no second dispatch after the gate closed)", runner.calls)
	}
}

// A dispatch error is ambiguous: the run row may have been admitted before the
// driver returned the error. When the latest register-run id moved across the
// failed dispatch, the retry must NOT re-dispatch beside the admitted run -
// fire-once wins over retry, and the restart reconcile owns any orphaned row.
func TestFirstFire_AdmittedRunBlocksRetry(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{db.ErrNotFound}, markerIDs: []int64{0, 5}}
	runner := &fakeFirstFireRunner{outs: []error{errTransient}}
	_, err := firstFire(store, runner, 1, 0)
	if err == nil {
		t.Fatal("firstFire error = nil, want the dispatch error (no retry beside an admitted run)")
	}
	if runner.calls != 1 {
		t.Errorf("runner.calls = %d, want 1 (must not re-dispatch beside an admitted register run)", runner.calls)
	}
}

// A register run left over from a PREVIOUS registration's failed first-fire
// must not block the retry: only a marker that moved across this dispatch
// signals an ambiguous admission.
func TestFirstFire_PriorRegisterRunDoesNotBlockRetry(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{db.ErrNotFound}, markerIDs: []int64{7, 7}}
	runner := &fakeFirstFireRunner{outs: []error{errTransient, nil}}
	id, err := firstFire(store, runner, 1, 0)
	if err != nil {
		t.Fatalf("firstFire error = %v, want nil (an unmoved marker must allow the retry)", err)
	}
	if id == 0 {
		t.Fatal("firstFire returned id 0, want a dispatched run id")
	}
	if runner.calls != 2 {
		t.Errorf("runner.calls = %d, want 2", runner.calls)
	}
}

// When the admission verification cannot complete (baseline or re-check
// errors), the state is uncertain, so the retry is skipped: never dispatch on
// state that cannot be verified.
func TestFirstFire_AdmittedCheckErrorBlocksRetry(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{db.ErrNotFound}, markerErrs: []error{errTransient}}
	runner := &fakeFirstFireRunner{outs: []error{errTransient}}
	_, err := firstFire(store, runner, 1, 0)
	if err == nil {
		t.Fatal("firstFire error = nil, want an error (uncertain admission state must not retry)")
	}
	if runner.calls != 1 {
		t.Errorf("runner.calls = %d, want 1", runner.calls)
	}
}

// A transient gate failure happens before any dispatch, so there is no
// admission to verify: the retry must proceed without consulting the
// admission marker at all (that query could fail under the same transient
// condition and wrongly kill the retry).
func TestFirstFire_GateErrorRetryNeedsNoAdmissionCheck(t *testing.T) {
	store := &fakeFirstFireStore{outs: []error{errTransient, db.ErrNotFound}, markerErrs: []error{errTransient}}
	runner := &fakeFirstFireRunner{outs: []error{nil}}
	id, err := firstFire(store, runner, 1, 0)
	if err != nil {
		t.Fatalf("firstFire error = %v, want nil (gate-error retry must not depend on the admission check)", err)
	}
	if id == 0 {
		t.Fatal("firstFire returned id 0, want a dispatched run id")
	}
	if store.markerCalls != 0 {
		t.Errorf("store.markerCalls = %d, want 0 (no dispatch was attempted, nothing to verify)", store.markerCalls)
	}
	if runner.calls != 1 {
		t.Errorf("runner.calls = %d, want 1", runner.calls)
	}
}
