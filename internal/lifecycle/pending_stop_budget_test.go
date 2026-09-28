package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// TestProcessPendingStops_TickBudgetRotatesEntries queues more unconfirmable
// stops than one tick's budget can retry. Each tick must stop starting
// retries once the budget is spent, and the entries it skipped must go first
// on the next tick, so every entry is retried once before any is retried
// twice. Every retry, of either manager-backed kind, must use the bounded
// manager call.
func TestProcessPendingStops_TickBudgetRotatesEntries(t *testing.T) {
	const entries = 5
	mgr := &fakeManager{stopIncarnationErrs: map[replicaKey][]error{}, stopIncarnationDelay: 50 * time.Millisecond}
	for i := 0; i < entries; i++ {
		mgr.stopIncarnationErrs[replicaKey{"app", i}] = []error{
			process.ErrStopUnconfirmed, process.ErrStopUnconfirmed, process.ErrStopUnconfirmed,
			process.ErrStopUnconfirmed, process.ErrStopUnconfirmed, process.ErrStopUnconfirmed,
		}
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), newFakeStore(map[string]*db.App{}, nil),
		func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.pendingStopRetry = 30 * time.Millisecond
	w.pendingStopTick = 100 * time.Millisecond
	for i := 0; i < entries; i++ {
		kind := pendingStopRecoveryUnready
		if i%2 == 1 {
			kind = pendingStopElasticHibernate
		}
		w.QueuePendingStop(PendingStopEntry{
			Kind: kind, Slug: "app", Index: i,
			AppID: 1, PID: 1000 + i, Incarnation: uint64(i + 1),
		})
	}

	w.processPendingStops()
	mgr.mu.Lock()
	firstTick := len(mgr.stopIncarnationCalls)
	mgr.mu.Unlock()
	if firstTick < 1 || firstTick >= entries {
		t.Fatalf("first tick retried %d of %d entries; want at least 1 and fewer than all, since the tick budget covers about 2 retries", firstTick, entries)
	}

	for tick := 0; tick < entries; tick++ {
		mgr.mu.Lock()
		n := len(mgr.stopIncarnationCalls)
		mgr.mu.Unlock()
		if n >= entries {
			break
		}
		w.processPendingStops()
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.stopIncarnationCalls) < entries {
		t.Fatalf("only %d retries after %d ticks", len(mgr.stopIncarnationCalls), entries+1)
	}
	seen := map[int]bool{}
	for _, c := range mgr.stopIncarnationCalls[:entries] {
		if seen[c.index] {
			t.Fatalf("index %d retried twice before every entry was retried once: %+v", c.index, mgr.stopIncarnationCalls)
		}
		seen[c.index] = true
	}
	if len(mgr.stopIncarnationBudgets) != len(mgr.stopIncarnationCalls) {
		t.Fatalf("bounded calls = %d, all calls = %d; every pending-stop retry must be bounded", len(mgr.stopIncarnationBudgets), len(mgr.stopIncarnationCalls))
	}
	for _, b := range mgr.stopIncarnationBudgets {
		if b != w.pendingStopRetry {
			t.Fatalf("retry budget = %v, want %v", b, w.pendingStopRetry)
		}
	}
}

// startTermIgnoringNativeProcess launches a process-group leader whose cwd is
// bundleDir and which logs one line to logPath for every SIGTERM it receives
// instead of exiting, so a recovery stop has to escalate to SIGKILL.
func startTermIgnoringNativeProcess(t *testing.T, bundleDir, logPath string) (int, <-chan error) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	cmd := exec.Command(sh, "-c", fmt.Sprintf(`trap 'echo T >> %q' TERM; echo ready >> %q; while :; do sleep 0.05; done`, logPath, logPath))
	cmd.Dir = bundleDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start native test process: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("native test process %d did not exit", cmd.Process.Pid)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for termLogCount(t, logPath, "ready") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("native test process never installed its TERM trap")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cmd.Process.Pid, done
}

func termLogCount(t *testing.T, logPath, line string) int {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if l == line {
			n++
		}
	}
	return n
}

// TestWatcher_ElasticRecoveryPendingStopIsBoundedAndEscalates drives a
// pendingStopElasticRecovery entry for a real worker that ignores SIGTERM.
// One retry must return within its budget instead of blocking through the
// whole TERM grace, a second retry inside the grace must not signal again,
// and once the grace has elapsed the next retry must escalate to SIGKILL,
// confirm the exit and clear the identity row.
func TestWatcher_ElasticRecoveryPendingStopIsBoundedAndEscalates(t *testing.T) {
	bundleDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "signals.log")
	pid, done := startTermIgnoringNativeProcess(t, bundleDir, logPath)

	st := newFakeStore(map[string]*db.App{}, []*db.Deployment{{ID: 7, BundleDir: bundleDir}})
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.pendingStopRetry = 100 * time.Millisecond
	w.QueuePendingStop(PendingStopEntry{
		Kind: pendingStopElasticRecovery, Slug: "app", Index: 3,
		AppID: 1, DeploymentID: 7, PID: pid,
		Reason: "recorded native worker identity did not stop during recovery",
	})
	key := pendingStopKey{slug: "app", index: 3, deploymentID: 7}

	start := time.Now()
	w.processPendingStops()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("one retry blocked for %v; it must return within its budget, not wait out the TERM grace", elapsed)
	}
	waitForTermLog(t, logPath, 1)
	if _, queued := w.pendingStops[key]; !queued {
		t.Fatal("entry dequeued although the worker is still alive")
	}

	w.processPendingStops()
	time.Sleep(200 * time.Millisecond)
	if n := termLogCount(t, logPath, "T"); n != 1 {
		t.Fatalf("SIGTERM delivered %d times; a retry inside the grace must not re-signal", n)
	}
	select {
	case <-done:
		t.Fatal("worker exited before the escalation; the test needs it to ignore SIGTERM")
	default:
	}

	// Age the recorded TERM past the grace instead of sleeping through it.
	w.mu.Lock()
	e := w.pendingStops[key]
	e.nativeStop.termSentAt = time.Now().Add(-recordedNativeStopGrace - time.Second)
	w.pendingStops[key] = e
	w.mu.Unlock()

	w.processPendingStops()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker survived the escalated retry; expected SIGKILL")
	}
	if n := termLogCount(t, logPath, "T"); n != 1 {
		t.Errorf("SIGTERM delivered %d times; the escalation must send SIGKILL, not another SIGTERM", n)
	}
	if _, queued := w.pendingStops[key]; queued {
		w.processPendingStops() // the exit can land just after the step's probe
	}
	if _, queued := w.pendingStops[key]; queued {
		t.Fatal("entry still queued after the worker was killed")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	found := false
	for _, d := range st.deletedDeploymentReplicaIdentities {
		if d.appID == 1 && d.deploymentID == 7 && d.index == 3 && d.pid == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DeleteDeploymentReplicaIdentity(1, 7, 3, %d), got %+v", pid, st.deletedDeploymentReplicaIdentities)
	}
}

func waitForTermLog(t *testing.T, logPath string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for termLogCount(t, logPath, "T") < want {
		if time.Now().After(deadline) {
			t.Fatalf("SIGTERM logged %d times, want %d", termLogCount(t, logPath, "T"), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
