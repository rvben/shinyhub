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

// Manager orchestrates scheduled command runs end-to-end: enforcing overlap
// policies, building run contexts, recording run rows, invoking the runtime,
// and updating final status.
type Manager struct {
	rt         process.Runtime
	store      Store
	secretsKey []byte
	appsDir    string
	appDataDir string

	mu      sync.Mutex
	locks   map[int64]*sync.Mutex   // per-schedule mutex for "skip"/"queue" policies
	queues  map[int64]chan struct{}  // per-schedule capacity-2 semaphore for "queue" policy (1 active + 1 queued)
	active  map[int64]context.CancelFunc // in-flight run cancels, keyed by run ID
}

// NewManager constructs a Manager. secretsKey may be nil if no env vars are encrypted.
func NewManager(rt process.Runtime, st Store, secretsKey []byte, appsDir, appDataDir string) *Manager {
	return &Manager{
		rt:         rt,
		store:      st,
		secretsKey: secretsKey,
		appsDir:    appsDir,
		appDataDir: appDataDir,
		locks:      make(map[int64]*sync.Mutex),
		queues:     make(map[int64]chan struct{}),
		active:     make(map[int64]context.CancelFunc),
	}
}

// lockFor returns the per-schedule mutex for the given schedule ID, creating
// it lazily. Must be called with m.mu held.
func (m *Manager) lockFor(schedID int64) *sync.Mutex {
	mu, ok := m.locks[schedID]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[schedID] = mu
	}
	return mu
}

// queueChan returns the per-schedule capacity-2 semaphore channel for the
// given schedule ID, creating it lazily. Capacity is two so the queue policy
// admits one active run plus one waiting behind it; further concurrent runs
// are recorded as skipped_overlap. A capacity of one would make queue behave
// identically to skip — every overlapping run dropped. Must be called with
// m.mu held.
func (m *Manager) queueChan(schedID int64) chan struct{} {
	ch, ok := m.queues[schedID]
	if !ok {
		ch = make(chan struct{}, 2)
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
	mu := m.lockFor(sched.ID)
	acquired := mu.TryLock()
	m.mu.Unlock()

	if !acquired {
		return m.recordSkipped(sched, trigger, userID)
	}

	runID, err := m.insertRunRow(sched, trigger, userID)
	if err != nil {
		mu.Unlock()
		return 0, err
	}

	go func() {
		defer mu.Unlock()
		m.execute(sched, app, runID, trigger, userID)
	}()

	return runID, nil
}

// runWithQueue serializes runs via a capacity-2 channel semaphore: at most
// one run executes at a time (per-schedule mutex) and at most one further
// run waits behind it. Any additional concurrent run finds the semaphore
// full and is recorded as skipped_overlap.
func (m *Manager) runWithQueue(sched *db.Schedule, app *db.App, trigger string, userID *int64) (int64, error) {
	m.mu.Lock()
	sem := m.queueChan(sched.ID)
	mu := m.lockFor(sched.ID)
	m.mu.Unlock()

	// Try to queue non-blocking. If channel is full, skip.
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

	go func() {
		defer func() { <-sem }()
		mu.Lock()
		defer mu.Unlock()
		m.execute(sched, app, runID, trigger, userID)
	}()

	return runID, nil
}

// runConcurrent launches a run without any concurrency controls.
func (m *Manager) runConcurrent(sched *db.Schedule, app *db.App, trigger string, userID *int64) (int64, error) {
	runID, err := m.insertRunRow(sched, trigger, userID)
	if err != nil {
		return 0, err
	}
	go m.execute(sched, app, runID, trigger, userID)
	return runID, nil
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
// and recording the final status. It is always called in a goroutine.
func (m *Manager) execute(sched *db.Schedule, app *db.App, runID int64, trigger string, userID *int64) {
	// Build timeout context.
	var ctx context.Context
	var cancel context.CancelFunc
	if sched.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(sched.TimeoutSeconds)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	m.registerActive(runID, cancel)
	defer m.unregisterActive(runID)

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
	info, runErr := m.rt.RunOnce(ctx, params, logWriter)

	// Determine final status.
	status := "succeeded"
	code := info.Code
	switch {
	case runErr != nil:
		status = "failed"
	case info.Signaled && ctx.Err() == context.DeadlineExceeded:
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
