package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// hibernateFailOnceThenConfirmRuntime is a process.Runtime whose very first
// Signal call fails (as if the initial SIGTERM delivery hit a transient
// signal(2) error) and every later Signal call succeeds, unblocking the
// paired Wait so it reports a confirmed exit. It models the exact shape of
// an elastic-hibernate teardown that could not be confirmed by the time the
// app's status had to move to hibernated: retryElasticHibernateStop retries
// StopReplicaIncarnation with, critically, no ClaimStopPending call (see
// watcher.go's retryElasticHibernateStop) - the unclaimed-fence pattern this test drives
// through the real manager rather than fakeManager, which never models
// stopPending/claim state at all.
type hibernateFailOnceThenConfirmRuntime struct {
	mu      sync.Mutex
	calls   int
	nextPID int
	exited  chan struct{}
	once    sync.Once
}

func newHibernateFailOnceThenConfirmRuntime() *hibernateFailOnceThenConfirmRuntime {
	return &hibernateFailOnceThenConfirmRuntime{nextPID: 90000, exited: make(chan struct{})}
}

func (r *hibernateFailOnceThenConfirmRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	pid := r.nextPID
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: fmt.Sprintf("%d", pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *hibernateFailOnceThenConfirmRuntime) Signal(process.RunHandle, syscall.Signal) error {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n == 1 {
		return errors.New("operation not permitted")
	}
	r.once.Do(func() { close(r.exited) })
	return nil
}

func (r *hibernateFailOnceThenConfirmRuntime) Wait(_ context.Context, _ process.RunHandle) error {
	<-r.exited
	return nil
}

func (r *hibernateFailOnceThenConfirmRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *hibernateFailOnceThenConfirmRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *hibernateFailOnceThenConfirmRuntime) HostPreparesDeps() bool    { return false }
func (r *hibernateFailOnceThenConfirmRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *hibernateFailOnceThenConfirmRuntime) HostProvidesAppData() bool { return false }

// TestRetryElasticHibernateStop_UnclaimedFenceFreesSlotOnRetry drives the
// pendingStopElasticHibernate retry path (watcher.go's
// retryElasticHibernateStop) against a REAL *process.Manager instead of
// fakeManager, so the assertions below exercise the manager's genuine
// stopPending-fence bookkeeping rather than fakeManager's simplified
// entry-removal simulation (which already always frees the slot and so
// cannot catch this defect).
//
// retryElasticHibernateStop never calls ClaimStopPending before or after its
// StopReplicaIncarnation retry - by design (see the pendingStopElasticHibernate
// doc comment): the proxy's
// slot high-water mark is what keeps an elastic slug/index pair from being
// reused while an entry for it is still queued, not a claimed fence. So once
// the retry confirms the exit, the manager entry must be removed outright and
// the slot must be immediately reusable via Start.
//
// Failing-first: before the fix, finalizeConfirmedStop removed an entry only
// when !e.stopPending. The first (failed) StopReplicaConfirmed call below
// sets stopPending=true and nothing ever claims or otherwise clears it, so
// the retry's successful StopReplicaConfirmed left the entry in place: the
// pendingStops queue entry was dequeued (retryElasticHibernateStop only
// checks the returned error, not the manager's internal fence state), but the
// manager slot stayed fenced forever, and the following mgr.Start returned
// ErrReplicaStopPending - the exact defect this test asserts against.
func TestRetryElasticHibernateStop_UnclaimedFenceFreesSlotOnRetry(t *testing.T) {
	const slug = "elastic-hibernate-demo"
	rt := newHibernateFailOnceThenConfirmRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)

	params := process.StartParams{Slug: slug, Index: 0, Command: []string{"x"}, Port: 1}
	info, err := mgr.Start(params)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	prx := newFakeProxy()
	st := newFakeStore(map[string]*db.App{slug: {ID: 1, Slug: slug, Status: "running"}}, nil)
	w := newTestWatcherWithManager(Config{}, mgr, prx, st,
		func(_ context.Context, s, dir string, idx int) (*deploy.Result, error) {
			return nil, errors.New("deploy not used")
		})

	// Reproduce hibernatePool's elastic branch: the first stop attempt at
	// hibernate time fails to confirm (the runtime's first Signal is
	// rejected), so the entry is queued for retry exactly as
	// hibernateElasticPool does on an unconfirmed TerminateConfirmed result.
	gen, _, ok := mgr.ReplicaIncarnation(slug, 0)
	if !ok {
		t.Fatal("no incarnation for the started replica")
	}
	if err := mgr.StopReplicaConfirmed(slug, 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}

	w.QueuePendingStop(PendingStopEntry{
		Kind:        pendingStopElasticHibernate,
		Slug:        slug,
		Index:       0,
		AppID:       1,
		PID:         info.PID,
		Incarnation: gen,
		Native:      false, // skip the identity-delete half; only the manager-fence half is under test
	})
	key := replicaKey{slug, 0}
	if !w.isPendingStop(key) {
		t.Fatal("expected the unconfirmed stop to be queued in pendingStops")
	}

	// Next tick: retryElasticHibernateStop's StopReplicaIncarnation retry now
	// succeeds (the runtime's second Signal call confirms the exit).
	w.processPendingStops()

	if w.isPendingStop(key) {
		t.Error("expected the entry to be dequeued once the retry confirmed the stop")
	}
	if _, _, ok := mgr.ReplicaIncarnation(slug, 0); ok {
		t.Fatal("manager still holds an entry after the elastic-hibernate retry's unclaimed fence confirmed the stop")
	}
	if _, err := mgr.Start(params); err != nil {
		t.Fatalf("Start after the elastic-hibernate retry confirmed the stop: %v", err)
	}
}
