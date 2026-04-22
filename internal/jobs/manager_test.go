package jobs_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

// fakeRuntime records RunOnce calls and supports a block channel to simulate
// slow runs. It embeds process.Runtime so unused interface methods compile.
type fakeRuntime struct {
	process.Runtime

	mu        sync.Mutex
	calls     int
	lastParams process.StartParams

	// block, when non-nil, is received by RunOnce before returning.
	// Set to a non-nil channel to simulate a long-running process.
	block chan struct{}

	exitInfo process.ExitInfo
	err      error
}

func (f *fakeRuntime) RunOnce(ctx context.Context, p process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.mu.Lock()
			f.calls++
			f.lastParams = p
			f.mu.Unlock()
			return process.ExitInfo{Code: -1, Signaled: true}, nil
		}
	}
	f.mu.Lock()
	f.calls++
	f.lastParams = p
	f.mu.Unlock()
	return f.exitInfo, f.err
}

// waitForCalls polls until rt.calls >= want or timeout expires.
func waitForCalls(t *testing.T, rt *fakeRuntime, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rt.mu.Lock()
		c := rt.calls
		rt.mu.Unlock()
		if c >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	rt.mu.Lock()
	got := rt.calls
	rt.mu.Unlock()
	t.Fatalf("waitForCalls: wanted %d calls, got %d after %v", want, got, timeout)
}

// fakeStore implements jobs.Store in-memory.
type fakeStore struct {
	mu sync.Mutex

	schedule    *db.Schedule
	app         *db.App
	envVars     []db.AppEnvVar
	mounts      []*db.SharedDataMount
	deployments []*db.Deployment

	runs        map[int64]*db.ScheduleRun
	nextRunID   int64
	finishCalls []db.FinishScheduleRunParams
	logPaths    []struct {
		RunID int64
		Path  string
	}
}

func newFakeStore(sched *db.Schedule, app *db.App) *fakeStore {
	return &fakeStore{
		schedule: sched,
		app:      app,
		// One deployment by default so tests don't have to wire one up unless
		// they care about the bundle dir specifically.
		deployments: []*db.Deployment{{ID: 1, AppID: app.ID, Version: "v1", BundleDir: "/tmp/fake-bundle"}},
		runs:        make(map[int64]*db.ScheduleRun),
		nextRunID:   1,
	}
}

func (f *fakeStore) GetSchedule(id int64) (*db.Schedule, error) {
	if f.schedule != nil && f.schedule.ID == id {
		return f.schedule, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) GetAppByID(id int64) (*db.App, error) {
	if f.app != nil && f.app.ID == id {
		return f.app, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) ListDeployments(appID int64) ([]*db.Deployment, error) {
	return f.deployments, nil
}

func (f *fakeStore) ListAppEnvVars(appID int64) ([]db.AppEnvVar, error) {
	return f.envVars, nil
}

func (f *fakeStore) ListSharedDataSources(consumerAppID int64) ([]*db.SharedDataMount, error) {
	return f.mounts, nil
}

func (f *fakeStore) InsertScheduleRun(p db.InsertScheduleRunParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextRunID
	f.nextRunID++
	run := &db.ScheduleRun{
		ID:                id,
		ScheduleID:        p.ScheduleID,
		Status:            p.Status,
		Trigger:           p.Trigger,
		TriggeredByUserID: p.TriggeredByUserID,
		StartedAt:         p.StartedAt,
		LogPath:           p.LogPath,
	}
	f.runs[id] = run
	return id, nil
}

func (f *fakeStore) SetScheduleRunLogPath(runID int64, logPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logPaths = append(f.logPaths, struct {
		RunID int64
		Path  string
	}{RunID: runID, Path: logPath})
	return nil
}

func (f *fakeStore) FinishScheduleRun(p db.FinishScheduleRunParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[p.RunID]
	if !ok {
		return db.ErrNotFound
	}
	r.Status = p.Status
	r.ExitCode = p.ExitCode
	t := p.FinishedAt
	r.FinishedAt = &t
	f.finishCalls = append(f.finishCalls, p)
	return nil
}

func (f *fakeStore) LogAuditEvent(p db.AuditEventParams) {}

// helpers

func makeSchedule(overlapPolicy string, timeoutSeconds int) *db.Schedule {
	return &db.Schedule{
		ID:             1,
		AppID:          10,
		Name:           "test-schedule",
		CronExpr:       "0 * * * *",
		CommandJSON:    `["echo","hello"]`,
		Enabled:        true,
		TimeoutSeconds: timeoutSeconds,
		OverlapPolicy:  overlapPolicy,
		MissedPolicy:   "skip",
	}
}

func makeApp() *db.App {
	return &db.App{
		ID:   10,
		Slug: "test-app",
		Name: "Test App",
	}
}

func newTestManager(t *testing.T, rt *fakeRuntime, st *fakeStore) *jobs.Manager {
	t.Helper()
	dir := t.TempDir()
	return jobs.NewManager(rt, st, nil, dir, dir)
}

// TestManager_Run_HappyPath verifies that a successful run (exit code 0)
// inserts a run row, invokes RunOnce, and marks it succeeded.
func TestManager_Run_HappyPath(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	m := newTestManager(t, rt, st)

	runID, err := m.Run(1, "manual", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("expected positive runID, got %d", runID)
	}

	waitForCalls(t, rt, 1, 2*time.Second)

	// Give manager time to finish the run
	time.Sleep(50 * time.Millisecond)

	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.finishCalls) == 0 {
		t.Fatal("expected FinishScheduleRun to be called")
	}
	fc := st.finishCalls[0]
	if fc.Status != "succeeded" {
		t.Errorf("expected status 'succeeded', got %q", fc.Status)
	}
	if fc.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", fc.ExitCode)
	}
}

// TestManager_Run_NonZeroExit_StatusFailed verifies that a non-zero exit code
// marks the run as failed with the correct exit code.
func TestManager_Run_NonZeroExit_StatusFailed(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 2}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	m := newTestManager(t, rt, st)

	runID, err := m.Run(1, "manual", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("expected positive runID, got %d", runID)
	}

	waitForCalls(t, rt, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.finishCalls) == 0 {
		t.Fatal("expected FinishScheduleRun to be called")
	}
	fc := st.finishCalls[0]
	if fc.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", fc.Status)
	}
	if fc.ExitCode != 2 {
		t.Errorf("expected exit code 2, got %d", fc.ExitCode)
	}
}

// TestManager_Run_OverlapSkip_DropsConcurrent verifies that when a run is
// already in-flight with overlap=skip, a second concurrent Run records a
// skipped_overlap row and returns without calling RunOnce again.
func TestManager_Run_OverlapSkip_DropsConcurrent(t *testing.T) {
	block := make(chan struct{})
	rt := &fakeRuntime{
		exitInfo: process.ExitInfo{Code: 0},
		block:    block,
	}
	st := newFakeStore(makeSchedule("skip", 30), makeApp())
	m := newTestManager(t, rt, st)

	// Start first run — it blocks until we close block.
	runID1, err := m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if runID1 <= 0 {
		t.Fatalf("expected positive runID for first run, got %d", runID1)
	}

	// Give goroutine time to acquire the lock before we try the second.
	time.Sleep(20 * time.Millisecond)

	// Second run should be skipped.
	runID2, err := m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if runID2 <= 0 {
		t.Fatalf("expected positive runID for skipped run, got %d", runID2)
	}

	// Verify that the skipped run was finished as skipped_overlap.
	st.mu.Lock()
	var skippedFinish *db.FinishScheduleRunParams
	for i := range st.finishCalls {
		if st.finishCalls[i].Status == "skipped_overlap" {
			skippedFinish = &st.finishCalls[i]
			break
		}
	}
	st.mu.Unlock()

	if skippedFinish == nil {
		t.Fatal("expected a FinishScheduleRun call with status 'skipped_overlap'")
	}

	// Unblock the first run.
	close(block)
	waitForCalls(t, rt, 1, 2*time.Second)
}

// TestManager_Run_OverlapQueue_QueuesOneAndSkipsRest exercises the
// overlap=queue policy: while a run is in flight, exactly one further run
// must be queued behind it; only the third (and beyond) must be recorded as
// skipped_overlap. Previously the per-schedule semaphore had capacity 1, so
// queue behaved identically to skip — every concurrent run was dropped
// instead of waiting for its turn.
func TestManager_Run_OverlapQueue_QueuesOneAndSkipsRest(t *testing.T) {
	block := make(chan struct{})
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}, block: block}
	st := newFakeStore(makeSchedule("queue", 30), makeApp())
	m := newTestManager(t, rt, st)

	// Run #1 — starts, blocks inside RunOnce.
	if _, err := m.Run(1, "cron", nil); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Let the goroutine reach RunOnce so the per-schedule lock is held.
	time.Sleep(50 * time.Millisecond)

	// Run #2 — must queue (sits waiting on the per-schedule lock).
	if _, err := m.Run(1, "cron", nil); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// Run #3 — semaphore is full (1 active + 1 queued), so this must skip.
	if _, err := m.Run(1, "cron", nil); err != nil {
		t.Fatalf("third Run: %v", err)
	}

	// Snapshot the inserted run statuses while #1 is still blocked.
	st.mu.Lock()
	var running, skipped int
	for _, r := range st.runs {
		switch r.Status {
		case "running":
			running++
		case "skipped_overlap":
			skipped++
		}
	}
	st.mu.Unlock()

	if running != 2 || skipped != 1 {
		t.Fatalf("queue policy must accept 1 active + 1 queued and skip the rest; got running=%d skipped=%d",
			running, skipped)
	}

	// Drain: closing the block channel releases the active run; the queued
	// run then acquires the lock, hits the (already-closed) block, and runs.
	close(block)
	waitForCalls(t, rt, 2, 2*time.Second)
}

// TestManager_Run_OverlapQueue_CancelOfQueuedRunSkipsExecution verifies that
// cancelling a queued (not-yet-running) overlap is honored: the run must be
// finalized as "cancelled" without ever invoking RunOnce. Prior to the fix,
// registerActive only happened inside execute, so a queued goroutine blocked
// on the per-schedule mutex had no entry in m.active and Cancel silently
// no-op'd; once the active run finished the queued run executed anyway.
func TestManager_Run_OverlapQueue_CancelOfQueuedRunSkipsExecution(t *testing.T) {
	block := make(chan struct{})
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}, block: block}
	st := newFakeStore(makeSchedule("queue", 30), makeApp())
	m := newTestManager(t, rt, st)

	// Run #1 — starts and blocks inside RunOnce, holding the per-schedule lock.
	id1, err := m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	_ = id1
	// Let the goroutine reach RunOnce so the per-schedule lock is held.
	// We can't waitForCalls here — fakeRuntime only increments calls
	// after the block channel closes.
	time.Sleep(50 * time.Millisecond)

	// Run #2 — queues behind #1 (semaphore admits, mu.Lock blocks).
	id2, err := m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// Cancel the queued run BEFORE the active run finishes. The cancel
	// must reach the queued goroutine via its registered context.
	if err := m.Cancel(id2); err != nil {
		t.Fatalf("Cancel queued run: %v", err)
	}

	// Release run #1. Run #2's goroutine acquires mu, observes ctx.Err()
	// is non-nil, finalises status="cancelled", and returns without
	// invoking RunOnce.
	close(block)

	// Poll for run #2 finalisation. We do not use waitForCalls because
	// rt.calls must stay at 1 — the assertion below depends on that.
	// Read FinishedAt and Status under the same lock that FinishScheduleRun
	// writes under to keep -race happy.
	deadline := time.Now().Add(2 * time.Second)
	var (
		r2Found  bool
		r2Status string
	)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		r := st.runs[id2]
		var finished bool
		if r != nil && r.FinishedAt != nil {
			finished = true
			r2Status = r.Status
		}
		st.mu.Unlock()
		if finished {
			r2Found = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !r2Found {
		t.Fatal("queued run #2 never finalised after cancel + release")
	}
	if r2Status != "cancelled" {
		t.Errorf("queued run after cancel: status=%q, want %q", r2Status, "cancelled")
	}

	rt.mu.Lock()
	calls := rt.calls
	rt.mu.Unlock()
	if calls != 1 {
		t.Errorf("RunOnce calls=%d, want 1 — queued run must not execute after cancel", calls)
	}
}

// TestManager_Run_PersistsLogPath verifies that Manager calls SetScheduleRunLogPath
// with a non-empty path after opening the log file for a run.
func TestManager_Run_PersistsLogPath(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	m := newTestManager(t, rt, st)

	if _, err := m.Run(1, "schedule", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForCalls(t, rt, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.logPaths) != 1 {
		t.Fatalf("expected 1 SetScheduleRunLogPath call, got %d", len(st.logPaths))
	}
	got := st.logPaths[0].Path
	if !strings.Contains(got, "run-") || !strings.HasSuffix(got, ".log") {
		t.Fatalf("expected log path containing run- and ending .log, got %q", got)
	}
}

// TestManager_Run_TimeoutMarksTimedOut verifies that when a run exceeds its
// timeout, RunOnce receives a cancelled context and the run is marked timed_out.
func TestManager_Run_TimeoutMarksTimedOut(t *testing.T) {
	// block is never closed; RunOnce will block until ctx expires.
	block := make(chan struct{})
	rt := &fakeRuntime{
		block: block,
		// exitInfo is not used — fakeRuntime returns Signaled=true on ctx.Done().
	}
	// TimeoutSeconds=1 so the context expires after 1 second.
	st := newFakeStore(makeSchedule("concurrent", 1), makeApp())
	m := newTestManager(t, rt, st)

	runID, err := m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("expected positive runID, got %d", runID)
	}

	// Wait for RunOnce to be called (ctx expires after 1s, then RunOnce returns).
	waitForCalls(t, rt, 1, 3*time.Second)
	time.Sleep(50 * time.Millisecond)

	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.finishCalls) == 0 {
		t.Fatal("expected FinishScheduleRun to be called")
	}
	fc := st.finishCalls[0]
	if fc.Status != "timed_out" {
		t.Errorf("expected status 'timed_out', got %q", fc.Status)
	}
}

// TestManager_Run_UsesLatestDeploymentBundleDir guards against regressing to a
// hardcoded "<appsDir>/<slug>/current" path; bundles are addressed via the
// deployment row's BundleDir, not a symlink that doesn't exist.
func TestManager_Run_UsesLatestDeploymentBundleDir(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	st.deployments = []*db.Deployment{
		// Newest first — Manager should pick deployments[0].
		{ID: 2, AppID: 10, Version: "v2", BundleDir: "/tmp/bundles/v2"},
		{ID: 1, AppID: 10, Version: "v1", BundleDir: "/tmp/bundles/v1"},
	}
	m := newTestManager(t, rt, st)

	if _, err := m.Run(1, "manual", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForCalls(t, rt, 1, 2*time.Second)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.lastParams.Dir != "/tmp/bundles/v2" {
		t.Fatalf("expected StartParams.Dir from latest deployment, got %q", rt.lastParams.Dir)
	}
}

// TestManager_Run_FailsWhenNoDeployments verifies Manager records a failed run
// (rather than panicking or running with an empty Dir) when the app has no
// deployment rows.
func TestManager_Run_FailsWhenNoDeployments(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	st.deployments = nil
	m := newTestManager(t, rt, st)

	if _, err := m.Run(1, "manual", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Wait for the run goroutine to call FinishScheduleRun.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		n := len(st.finishCalls)
		st.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.finishCalls) == 0 {
		t.Fatal("expected FinishScheduleRun call")
	}
	if got := st.finishCalls[0].Status; got != "failed" {
		t.Errorf("expected status 'failed', got %q", got)
	}
	if rt.calls != 0 {
		t.Errorf("expected RunOnce not to be called when no deployments, got %d calls", rt.calls)
	}
}
