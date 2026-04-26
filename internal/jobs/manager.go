package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/secrets"
)

// schedLock is a context-aware mutex used to serialize runs of a single
// schedule. Unlike sync.Mutex it supports cancellable acquisition: a
// caller can give up waiting if its context is cancelled, releasing
// resources (notably the queue-semaphore slot) without first waiting
// for the active run to finish.
//
// Implemented as a 1-buffered channel: send = lock, receive = unlock.
type schedLock struct {
	ch chan struct{}
}

func newSchedLock() *schedLock { return &schedLock{ch: make(chan struct{}, 1)} }

// tryLock acquires the lock without blocking. Returns true on success.
func (l *schedLock) tryLock() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// lock blocks until the lock is acquired or ctx is cancelled. Returns
// true on acquisition, false if ctx finished first.
//
// Cancellation always wins over acquisition: when the active holder
// releases the lock at the same instant that ctx.Done() fires, Go's
// select picks one of the two ready cases at random. Without the
// post-acquisition re-check, a cancelled queued run would sometimes
// take the slot and execute anyway. Recover by releasing the lock and
// reporting cancellation.
func (l *schedLock) lock(ctx context.Context) bool {
	select {
	case l.ch <- struct{}{}:
		if ctx.Err() != nil {
			<-l.ch
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

// unlock releases the lock. Must only be called by a goroutine that
// previously acquired it via tryLock or lock.
func (l *schedLock) unlock() { <-l.ch }

// Manager orchestrates scheduled command runs end-to-end: enforcing overlap
// policies, building run contexts, recording run rows, invoking the runtime,
// and updating final status.
type Manager struct {
	rt         process.Runtime
	store      Store
	secretsKey []byte
	appsDir    string
	appDataDir string

	mu     sync.Mutex
	locks  map[int64]*schedLock         // per-schedule lock for "skip"/"queue" policies
	queues map[int64]chan struct{}      // per-schedule capacity-2 semaphore for "queue" policy (1 active + 1 queued)
	active map[int64]context.CancelFunc // in-flight run cancels, keyed by run ID
}

// NewManager constructs a Manager. secretsKey may be nil if no env vars are encrypted.
func NewManager(rt process.Runtime, st Store, secretsKey []byte, appsDir, appDataDir string) *Manager {
	return &Manager{
		rt:         rt,
		store:      st,
		secretsKey: secretsKey,
		appsDir:    appsDir,
		appDataDir: appDataDir,
		locks:      make(map[int64]*schedLock),
		queues:     make(map[int64]chan struct{}),
		active:     make(map[int64]context.CancelFunc),
	}
}

// lockFor returns the per-schedule lock for the given schedule ID, creating
// it lazily. Must be called with m.mu held.
func (m *Manager) lockFor(schedID int64) *schedLock {
	l, ok := m.locks[schedID]
	if !ok {
		l = newSchedLock()
		m.locks[schedID] = l
	}
	return l
}

// queueChan returns the per-schedule capacity-1 semaphore channel for the
// given schedule ID, creating it lazily. The semaphore counts only queued
// (waiting) runs — the active run is tracked separately by the per-schedule
// schedLock. Capacity one therefore means "at most one waiting run behind
// the active one"; further concurrent runs are recorded as skipped_overlap.
// Must be called with m.mu held.
func (m *Manager) queueChan(schedID int64) chan struct{} {
	ch, ok := m.queues[schedID]
	if !ok {
		ch = make(chan struct{}, 1)
		m.queues[schedID] = ch
	}
	return ch
}

// registerActive records the cancel function for an in-flight run so it can be
// stopped via Cancel.
func (m *Manager) registerActive(runID int64, cancel context.CancelFunc) {
	m.mu.Lock()
	m.active[runID] = cancel
	m.mu.Unlock()
}

// unregisterActive removes a completed run from the active set.
func (m *Manager) unregisterActive(runID int64) {
	m.mu.Lock()
	delete(m.active, runID)
	m.mu.Unlock()
}

// Cancel signals an in-flight run to terminate. Returns nil even when the run
// has already finished — cancellation is best-effort.
func (m *Manager) Cancel(runID int64) error {
	m.mu.Lock()
	cancel, ok := m.active[runID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// Run executes a scheduled run for the given schedule ID. It enforces the
// schedule's overlap policy and returns the run row ID even when a run is
// skipped. The actual command execution happens in a background goroutine;
// Run returns as soon as the run row is inserted (or the skipped row is
// finished).
func (m *Manager) Run(scheduleID int64, trigger string, userID *int64) (int64, error) {
	sched, err := m.store.GetSchedule(scheduleID)
	if err != nil {
		return 0, fmt.Errorf("get schedule %d: %w", scheduleID, err)
	}
	app, err := m.store.GetAppByID(sched.AppID)
	if err != nil {
		return 0, fmt.Errorf("get app %d: %w", sched.AppID, err)
	}

	switch sched.OverlapPolicy {
	case "skip":
		return m.runWithSkip(sched, app, trigger, userID)
	case "queue":
		return m.runWithQueue(sched, app, trigger, userID)
	case "concurrent":
		return m.runConcurrent(sched, app, trigger, userID)
	default:
		return 0, fmt.Errorf("unknown overlap_policy %q", sched.OverlapPolicy)
	}
}

// runWithSkip launches a run only if no other run for this schedule is active.
// If one is already running, it records a skipped_overlap row and returns.
func (m *Manager) runWithSkip(sched *db.Schedule, app *db.App, trigger string, userID *int64) (int64, error) {
	m.mu.Lock()
	slot := m.lockFor(sched.ID)
	acquired := slot.tryLock()
	m.mu.Unlock()

	if !acquired {
		return m.recordSkipped(sched, trigger, userID)
	}

	runID, err := m.insertRunRow(sched, trigger, userID)
	if err != nil {
		slot.unlock()
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	m.registerActive(runID, cancel)

	go func() {
		defer slot.unlock()
		defer cancel()
		defer m.unregisterActive(runID)
		m.execute(ctx, sched, app, runID, trigger, userID)
	}()

	return runID, nil
}

// runWithQueue serializes runs of one schedule with at most one active and
// at most one waiting behind it. Additional concurrent runs are recorded as
// skipped_overlap.
//
// The schedule lock is acquired synchronously inside Run() — never inside
// the launched goroutine — so admission order is preserved across
// back-to-back triggers. If two callers race to admit at the same time,
// the channel send is atomic and one wins cleanly; without synchronous
// acquisition the launched goroutines could run in either order regardless
// of which Run() returned first.
//
// The cancel context is registered up-front, before slot.lock starts to
// wait, so a Cancel() RPC reaches a still-queued run. The acquisition
// itself is context-aware: when Cancel arrives while a run is queued,
// slot.lock returns false and the goroutine frees its semaphore slot and
// finalises the run as cancelled without first waiting for the active run
// to release the lock.
func (m *Manager) runWithQueue(sched *db.Schedule, app *db.App, trigger string, userID *int64) (int64, error) {
	m.mu.Lock()
	sem := m.queueChan(sched.ID)
	slot := m.lockFor(sched.ID)
	m.mu.Unlock()

	// Fast path: if the slot is free, become the active run synchronously
	// here in the caller's goroutine. This locks in admission order before
	// any goroutine is launched.
	if slot.tryLock() {
		runID, err := m.insertRunRow(sched, trigger, userID)
		if err != nil {
			slot.unlock()
			return 0, err
		}
		ctx, cancel := m.buildRunContext()
		m.registerActive(runID, cancel)
		go func() {
			defer slot.unlock()
			defer cancel()
			defer m.unregisterActive(runID)
			m.execute(ctx, sched, app, runID, trigger, userID)
		}()
		return runID, nil
	}

	// Slot is held by an active run. Try to take the single queue slot.
	select {
	case sem <- struct{}{}:
	default:
		return m.recordSkipped(sched, trigger, userID)
	}

	runID, err := m.insertRunRow(sched, trigger, userID)
	if err != nil {
		<-sem
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	m.registerActive(runID, cancel)

	go func() {
		defer cancel()
		defer m.unregisterActive(runID)

		if !slot.lock(ctx) {
			// Cancel arrived while waiting in the queue. Free the
			// queue slot first so a new admission can take our place,
			// then finalise as cancelled. We never acquired the slot,
			// so do not call slot.unlock.
			<-sem
			m.finishRun(sched, runID, "cancelled", 0, trigger, userID)
			return
		}
		// Promoted from queued to active. Free the queue slot now —
		// not at goroutine exit — so one new run can wait behind us
		// during execution. Holding sem through execute would collapse
		// the queue policy to "skip" (one active, no queued).
		<-sem
		defer slot.unlock()
		m.execute(ctx, sched, app, runID, trigger, userID)
	}()

	return runID, nil
}

// runConcurrent launches a run without any concurrency controls.
func (m *Manager) runConcurrent(sched *db.Schedule, app *db.App, trigger string, userID *int64) (int64, error) {
	runID, err := m.insertRunRow(sched, trigger, userID)
	if err != nil {
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	m.registerActive(runID, cancel)

	go func() {
		defer cancel()
		defer m.unregisterActive(runID)
		m.execute(ctx, sched, app, runID, trigger, userID)
	}()

	return runID, nil
}

// buildRunContext returns a cancel-only context used for the lifetime of
// one schedule run from queue admission to completion. The schedule's
// per-run timeout is applied later, inside execute, so the timeout
// window starts when the runtime actually runs — not while the run is
// still waiting in the queue. The caller owns cancel and must defer it
// (the same cancel is registered in m.active so a Cancel() RPC can
// trigger it from outside).
func (m *Manager) buildRunContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// insertRunRow creates a schedule_runs row with status "running" and returns its ID.
func (m *Manager) insertRunRow(sched *db.Schedule, trigger string, userID *int64) (int64, error) {
	return m.store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID:        sched.ID,
		Status:            "running",
		Trigger:           trigger,
		TriggeredByUserID: userID,
		StartedAt:         time.Now().UTC(),
		LogPath:           "", // updated after log file creation
	})
}

// recordSkipped inserts a run row and immediately finishes it with
// status "skipped_overlap". Returns the run ID.
func (m *Manager) recordSkipped(sched *db.Schedule, trigger string, userID *int64) (int64, error) {
	runID, err := m.store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID:        sched.ID,
		Status:            "skipped_overlap",
		Trigger:           trigger,
		TriggeredByUserID: userID,
		StartedAt:         time.Now().UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("insert skipped run: %w", err)
	}
	if err := m.store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID:      runID,
		Status:     "skipped_overlap",
		ExitCode:   0,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		return runID, fmt.Errorf("finish skipped run: %w", err)
	}
	return runID, nil
}

// execute performs the actual command run: building params, calling RunOnce,
// and recording the final status. It is always called in a goroutine. ctx
// is owned and cancelled by the caller; execute does not register/unregister
// it, because for the queue policy the cancel must be observable while the
// goroutine is still blocked acquiring the per-schedule lock (see
// runWithQueue).
//
// The schedule's per-run timeout is applied here, deriving runCtx from
// ctx. Timing the timeout from execute (rather than from queue admission)
// guarantees a queued run gets its full configured timeout window once
// it actually starts running.
func (m *Manager) execute(ctx context.Context, sched *db.Schedule, app *db.App, runID int64, trigger string, userID *int64) {
	runCtx := ctx
	if sched.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(sched.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	// Build log file path and create directory.
	logDir := filepath.Join(m.appsDir, app.Slug, "schedules", fmt.Sprintf("%d", sched.ID))
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("run-%d.log", runID))

	logFile, err := os.Create(logPath)
	if err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	defer logFile.Close()

	if err := m.store.SetScheduleRunLogPath(runID, logPath); err != nil {
		slog.Warn("set schedule run log path", "run", runID, "err", err)
		// Non-fatal — proceed with the run. The streaming endpoint will fall back gracefully.
	}

	// Build env vars, decrypting secrets.
	envVars, err := m.store.ListAppEnvVars(app.ID)
	if err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	env := make([]string, 0, len(envVars))
	for _, v := range envVars {
		val := string(v.Value)
		if v.IsSecret && len(m.secretsKey) > 0 {
			plain, err := secrets.Decrypt(m.secretsKey, v.Value)
			if err == nil {
				val = string(plain)
			}
		}
		env = append(env, v.Key+"="+val)
	}

	// Build shared mounts.
	mounts, err := m.store.ListSharedDataSources(app.ID)
	if err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	sharedMounts := make([]process.SharedMount, 0, len(mounts))
	for _, r := range mounts {
		sharedMounts = append(sharedMounts, process.SharedMount{
			SourceSlug: r.SourceSlug,
			HostPath:   filepath.Join(m.appDataDir, r.SourceSlug),
		})
	}

	// Parse command from JSON.
	var cmd []string
	if err := json.Unmarshal([]byte(sched.CommandJSON), &cmd); err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}

	// Build app data dir.
	appDataPath := filepath.Join(m.appDataDir, app.Slug)
	if err := os.MkdirAll(appDataPath, 0o750); err != nil {
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}

	// Resolve the active bundle dir from the latest deployment. There is no
	// "current" symlink on disk — bundles live at versions/<version>/ and the
	// authoritative pointer is the most-recent deployment row.
	deployments, err := m.store.ListDeployments(app.ID)
	if err != nil {
		fmt.Fprintf(logFile, "shinyhub: list deployments: %v\n", err)
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	if len(deployments) == 0 {
		fmt.Fprintf(logFile, "shinyhub: app %q has no deployments; cannot run schedule\n", app.Slug)
		m.finishRun(sched, runID, "failed", 0, trigger, userID)
		return
	}
	bundleDir := deployments[0].BundleDir

	params := process.StartParams{
		Slug:         app.Slug,
		Dir:          bundleDir,
		Command:      cmd,
		Env:          env,
		AppDataPath:  appDataPath,
		SharedMounts: sharedMounts,
	}

	// Run the command.
	var logWriter io.Writer = logFile
	info, runErr := m.rt.RunOnce(runCtx, params, logWriter)

	// Determine final status.
	status := "succeeded"
	code := info.Code
	switch {
	case runErr != nil:
		status = "failed"
	case info.Signaled && runCtx.Err() == context.DeadlineExceeded:
		status = "timed_out"
	case info.Signaled:
		status = "cancelled"
	case info.Code != 0:
		status = "failed"
	}

	m.finishRun(sched, runID, status, code, trigger, userID)
}

// finishRun updates the run row and logs an audit event.
func (m *Manager) finishRun(sched *db.Schedule, runID int64, status string, exitCode int, trigger string, userID *int64) {
	_ = m.store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID:      runID,
		Status:     status,
		ExitCode:   exitCode,
		FinishedAt: time.Now().UTC(),
	})

	m.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       "schedule_run_" + status,
		ResourceType: "schedule",
		ResourceID:   fmt.Sprintf("%d", sched.ID),
		Detail:       fmt.Sprintf(`{"trigger":%q,"run_id":%d}`, trigger, runID),
	})
}
