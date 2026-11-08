package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
)

// heldBuilds stands in for uv sync. It counts how many builds are inside it at
// once and holds every one of them until the test opens the release channel,
// so the peak it records is a property of the gate, not of scheduling luck.
type heldBuilds struct {
	active, peak, entered atomic.Int32
	release               chan struct{}
}

func newHeldBuilds() *heldBuilds { return &heldBuilds{release: make(chan struct{})} }

func (h *heldBuilds) sync(context.Context, string, []string) error {
	n := h.active.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	h.entered.Add(1)
	<-h.release
	h.active.Add(-1)
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// eventLog collects deploy events from concurrent builds.
type eventLog struct {
	mu     sync.Mutex
	events []deployevent.Event
}

func (l *eventLog) record(e deployevent.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) count(match func(deployevent.Event) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if match(e) {
			n++
		}
	}
	return n
}

// A wait at the gate is its own phase, so every consumer renders it with the
// rules it already has for phases: the CLI prints the start line even off a
// terminal and shows a spinner on one, and the completion closes it.
func isQueuedEvent(e deployevent.Event) bool {
	return e.Phase == "build_queue" && e.Status == deployevent.StatusStarted &&
		strings.Contains(e.Message, "Waiting for a build slot")
}

func isSlotAcquiredEvent(e deployevent.Event) bool {
	return e.Phase == "build_queue" && e.Status == deployevent.StatusCompleted
}

// startBuilds runs n Python builds concurrently, each with the given budget,
// and returns a wait func that reports every build's error.
func startBuilds(t *testing.T, n int, budget time.Duration, progress func(deployevent.Event)) (wait func() []error) {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		dir := t.TempDir()
		go func() {
			defer wg.Done()
			errs[i] = buildEnvironment(Params{Slug: fmt.Sprintf("app-%d", i), BundleDir: dir, Progress: progress}, "python", budget)
		}()
	}
	return func() []error { wg.Wait(); return errs }
}

// At most max_concurrent_builds dependency builds run on the host at once; the
// rest wait at the gate and say so in the deploy's progress stream.
func TestBuildEnvironment_BoundsConcurrentBuildsToTheGate(t *testing.T) {
	defer SetMaxConcurrentBuildsForTest(2)()
	defer SetEnsureProjectForTest(func(context.Context, string, []string) error { return nil })()
	held := newHeldBuilds()
	defer SetSyncHooksForTest(held.sync, nil)()
	var log eventLog

	wait := startBuilds(t, 5, time.Minute, log.record)
	waitFor(t, "two builds to be running", func() bool { return held.entered.Load() >= 2 })
	waitFor(t, "three builds to queue at the gate", func() bool { return activeBuildGate.Load().waiting() == 3 })
	waitFor(t, "each queued build to report its wait", func() bool { return log.count(isQueuedEvent) == 3 })
	if got := held.active.Load(); got != 2 {
		t.Fatalf("%d builds running with the gate full, want 2", got)
	}

	close(held.release)
	for i, err := range wait() {
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if got := held.entered.Load(); got != 5 {
		t.Fatalf("%d builds ran, want all 5", got)
	}
	if got := held.peak.Load(); got != 2 {
		t.Fatalf("peak concurrency %d, want 2", got)
	}
	if got := log.count(isQueuedEvent); got != 3 {
		t.Fatalf("%d queued events in total, want exactly one per build that waited (3)", got)
	}
	if got := log.count(isSlotAcquiredEvent); got != 3 {
		t.Fatalf("%d slot-acquired events, want one closing each wait (3)", got)
	}
}

// With the cap removed every build runs at once, which also proves the
// harness above can observe concurrency beyond the gate.
func TestBuildEnvironment_UnlimitedGateRunsEveryBuildAtOnce(t *testing.T) {
	defer SetMaxConcurrentBuildsForTest(0)()
	defer SetEnsureProjectForTest(func(context.Context, string, []string) error { return nil })()
	held := newHeldBuilds()
	defer SetSyncHooksForTest(held.sync, nil)()
	var log eventLog

	wait := startBuilds(t, 5, time.Minute, log.record)
	waitFor(t, "all five builds to be running", func() bool { return held.entered.Load() == 5 })
	close(held.release)
	for i, err := range wait() {
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if got := held.peak.Load(); got != 5 {
		t.Fatalf("peak concurrency %d, want 5", got)
	}
	if got := log.count(isQueuedEvent) + log.count(isSlotAcquiredEvent); got != 0 {
		t.Fatalf("%d build_queue events without a cap, want none", got)
	}
}

// Time spent waiting for a slot is not build time: the build budget starts
// when the gate opens, so a long queue cannot fail a build before it begins.
func TestBuildEnvironment_BuildBudgetStartsWhenTheGateOpens(t *testing.T) {
	defer SetMaxConcurrentBuildsForTest(1)()
	defer SetEnsureProjectForTest(func(context.Context, string, []string) error { return nil })()
	held := newHeldBuilds()
	remaining := make(chan time.Duration, 1)
	defer SetSyncHooksForTest(func(ctx context.Context, dir string, env []string) error {
		if strings.HasSuffix(dir, "second") {
			if err := ctx.Err(); err != nil {
				return err
			}
			deadline, _ := ctx.Deadline()
			remaining <- time.Until(deadline)
			return nil
		}
		return held.sync(ctx, dir, env)
	}, nil)()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- buildEnvironment(Params{Slug: "first", BundleDir: t.TempDir()}, "python", time.Minute)
	}()
	waitFor(t, "the first build to hold the slot", func() bool { return held.entered.Load() == 1 })

	const budget = 50 * time.Millisecond
	var log eventLog
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- buildEnvironment(Params{Slug: "second", BundleDir: t.TempDir() + "/second", Progress: log.record}, "python", budget)
	}()
	waitFor(t, "the second build to queue", func() bool { return activeBuildGate.Load().waiting() == 1 })
	time.Sleep(3 * budget)
	close(held.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first build: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second build failed after queueing longer than its budget: %v", err)
	}
	if left := <-remaining; left < budget/2 {
		t.Fatalf("second build entered uv sync with %v of its %v budget left; the clock must start after the gate opens", left, budget)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	queued, acquired, started := -1, -1, -1
	for i, e := range log.events {
		switch {
		case isQueuedEvent(e) && queued < 0:
			queued = i
		case isSlotAcquiredEvent(e):
			acquired = i
		case e.Phase == "dependencies" && e.Status == deployevent.StatusStarted:
			started = i
		}
	}
	if queued < 0 || acquired < 0 || started < 0 || !(queued < acquired && acquired < started) {
		t.Fatalf("want queued (index %d) then slot acquired (index %d) then build start (index %d) in %+v", queued, acquired, started, log.events)
	}
	if got := log.events[acquired].Message; !strings.HasPrefix(got, "Build slot acquired") {
		t.Fatalf("slot-acquired event message %q, want it to say the slot was acquired", got)
	}
}

// A waiter that gives up leaves the gate as it found it.
func TestBuildGate_AcquireStopsWhenTheContextEnds(t *testing.T) {
	g := newBuildGate(1)
	release, err := g.acquire(context.Background(), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.acquire(ctx, time.Hour, nil); err == nil {
		t.Fatal("acquire on a full gate with a cancelled context must fail")
	}
	if got := g.waiting(); got != 0 {
		t.Fatalf("%d waiters recorded after the waiter gave up, want 0", got)
	}
	release()
	if got := g.running(); got != 0 {
		t.Fatalf("%d running after release, want 0", got)
	}
	if _, err := g.acquire(context.Background(), time.Hour, nil); err != nil {
		t.Fatalf("the released slot must be reusable: %v", err)
	}
}
