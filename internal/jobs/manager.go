package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/appenv"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/schedulespec"
	"golang.org/x/sys/unix"
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

// scheduleRunRecorder receives terminal scheduled-run outcomes for metrics.
// A local interface keeps the jobs package free of a metrics import.
type scheduleRunRecorder interface {
	RecordScheduleRun(slug, schedule, status string)
}

// Manager orchestrates scheduled command runs end-to-end: enforcing overlap
// policies, building run contexts, recording run rows, invoking the runtime,
// and updating final status.
type Manager struct {
	procMgr     *process.Manager
	tierOrder   []string
	defaultTier string
	store       Store
	metrics     scheduleRunRecorder
	secretsKey  []byte
	appsDir     string
	appDataDir  string

	// resolveResources resolves an app's effective per-replica memory/CPU limits
	// (per-app value over the runtime default) so a scheduled job is capped by the
	// same ceiling as its replicas. nil leaves jobs uncapped (the pre-existing
	// behavior). Set once at startup via SetResourceResolver, before any run.
	resolveResources       func(app *db.App) (memoryMB, cpuPct int)
	defaultWorkerIsolation string
	onUnsafePublication    func(appID int64, slug, status string)

	// globalSem caps the number of schedule runs executing simultaneously
	// across every schedule. overlap_policy "concurrent" otherwise spawns an
	// unbounded goroutine+process per trigger; this bounds the host/container
	// blast radius. Runs in excess of the cap wait (cheap goroutines) rather
	// than starting a process.
	globalSem chan struct{}

	mu       sync.Mutex
	stopped  bool                    // set by Stop; rejects new admissions
	draining bool                    // owner handoff: rejects admissions until active runs finalize
	locks    map[int64]*schedLock    // per-schedule lock for "skip"/"queue" policies
	queues   map[int64]chan struct{} // per-schedule capacity-2 semaphore for "queue" policy (1 active + 1 queued)
	// admissionLocks order the handoff into producerGates. They let an async
	// required writer reserve its place before returning to the dispatcher, so a
	// later deployment barrier cannot overtake it while preserving async APIs.
	admissionLocks map[int64]*sync.Mutex
	// producerGates allow ordinary concurrent runs to coexist as readers while
	// required convergence work is an exclusive writer. This makes a required
	// producer serialize with every execution regardless of overlap_policy.
	producerGates map[int64]*sync.RWMutex
	// publicationGates serialize every serving-data writer against consumer
	// startup for the app. Writers hold the exclusive side through durable run
	// completion; fresh consumers hold the shared side through replica upsert.
	publicationGates   map[int64]*sync.RWMutex
	publicationLockDir string
	active             map[int64]context.CancelFunc // in-flight run cancels, keyed by run ID
	activeApps         map[int64]int64              // run ID -> app ID for app-scoped deletion drains
	blockedApps        map[int64]bool               // permanently closes admissions for an app being deleted
	appActive          map[int64]int                // active/queued runs per app
	appDrained         map[int64]chan struct{}      // closed when an app's active count reaches zero

	// operatorCancelled records run IDs an operator explicitly cancelled via
	// Cancel, so finishRun can tell an operator cancel ("cancelled", terminal)
	// apart from a service-shutdown cancel ("interrupted", re-fired by the
	// startup reconcile). Entries are cleared in unregisterActive.
	operatorCancelled map[int64]bool

	// wg tracks every launched run goroutine so Stop can wait for them to
	// observe cancellation and finalize their DB rows before the process exits.
	wg sync.WaitGroup
	// terminalCtx bounds durable terminal-state retries only when shutdown's
	// caller gives up. During normal operation a transient store outage can last
	// arbitrarily long without turning a successful run into lost activation.
	terminalCtx    context.Context
	terminalCancel context.CancelFunc
}

// SetDefaultWorkerIsolation supplies the fleet fallback used when an app
// inherits worker isolation. Producer execution revalidates this at the
// physical write boundary so a configuration-only change cannot turn a
// previously safe multiplex app into an untracked elastic pool.
func (m *Manager) SetDefaultWorkerIsolation(mode string) {
	m.defaultWorkerIsolation = mode
}

// ErrManagerStopped is returned by Run once Stop has been called.
var ErrManagerStopped = fmt.Errorf("jobs manager stopped")

// ErrAppDeploying prevents an ordinary cron/manual admission from snapshotting
// the old deployment while a candidate producer barrier is in progress.
var ErrAppDeploying = fmt.Errorf("app deployment is in progress")

// ErrAppCompatibilityQuarantined prevents old-bundle consumers or jobs from
// running after a failed candidate may have published incompatible shared data
// or bundle-relative schedule declarations.
var ErrAppCompatibilityQuarantined = fmt.Errorf("app is compatibility-quarantined; deploy or roll back successfully before running consumers or schedules")

// defaultMaxConcurrentRuns bounds total simultaneously executing schedule
// runs across the manager.
const defaultMaxConcurrentRuns = 32

// NewManager constructs a Manager. secretsKey may be nil if no env vars are encrypted.
// tierOrder controls which tier jobs are routed to: the lowest-indexed tier in
// the app's placement is selected. defaultTier is used as a fallback when the
// placement is empty or yields no assignments.
//
// appDataDir is normalized to an absolute path so that scheduled runs always
// hand the runtime an absolute SHINYHUB_APP_DATA value. Without this, a
// relative value (e.g. "./data/app-data" from shinyhub.yaml) would resolve
// through the bundle-dir-side `data` symlink at run time and produce a
// doubly-nested path inside the persistent data dir. process.Manager applies
// the same normalization in SetAppDataRoot — the two must agree.
func NewManager(procMgr *process.Manager, tierOrder []string, defaultTier string, st Store, secretsKey []byte, appsDir, appDataDir string) (*Manager, error) {
	if appDataDir != "" {
		abs, err := filepath.Abs(appDataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve app data dir: %w", err)
		}
		appDataDir = abs
	}
	lockRoot := appDataDir
	if lockRoot == "" {
		lockRoot = appsDir
	}
	publicationLockDir := filepath.Join(lockRoot, ".shinyhub-locks")
	if err := os.MkdirAll(publicationLockDir, 0o750); err != nil {
		return nil, fmt.Errorf("create publication lock directory: %w", err)
	}
	terminalCtx, terminalCancel := context.WithCancel(context.Background())
	return &Manager{
		procMgr:            procMgr,
		tierOrder:          tierOrder,
		defaultTier:        defaultTier,
		store:              st,
		secretsKey:         secretsKey,
		appsDir:            appsDir,
		appDataDir:         appDataDir,
		globalSem:          make(chan struct{}, defaultMaxConcurrentRuns),
		locks:              make(map[int64]*schedLock),
		queues:             make(map[int64]chan struct{}),
		admissionLocks:     make(map[int64]*sync.Mutex),
		producerGates:      make(map[int64]*sync.RWMutex),
		publicationGates:   make(map[int64]*sync.RWMutex),
		publicationLockDir: publicationLockDir,
		active:             make(map[int64]context.CancelFunc),
		activeApps:         make(map[int64]int64),
		blockedApps:        make(map[int64]bool),
		appActive:          make(map[int64]int),
		appDrained:         make(map[int64]chan struct{}),
		operatorCancelled:  make(map[int64]bool),
		terminalCtx:        terminalCtx,
		terminalCancel:     terminalCancel,
	}, nil
}

// SetResourceResolver installs the closure that resolves an app's effective
// per-replica memory/CPU limits so scheduled jobs inherit the same ceiling as the
// app's replicas. Call once at startup before any run is dispatched.
func (m *Manager) SetResourceResolver(fn func(app *db.App) (memoryMB, cpuPct int)) {
	m.resolveResources = fn
}

// SetScheduleMetrics wires the metrics recorder that finishRun pushes terminal
// run outcomes to. Nil-safe: when unset (metrics disabled), no metric is
// recorded. Must be called before runs start.
func (m *Manager) SetScheduleMetrics(rec scheduleRunRecorder) { m.metrics = rec }

// SetUnsafePublicationHandler installs the fail-closed serving hook invoked
// after a physical data writer durably records a non-success outcome.
func (m *Manager) SetUnsafePublicationHandler(fn func(appID int64, slug, status string)) {
	m.onUnsafePublication = fn
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

// admissionLockFor must be called with m.mu held. The mutex orders attempts to
// enter the per-schedule producer gate. sync.RWMutex does not promise writer
// preference, so the separate admission mutex is what prevents a later reader
// or deployment barrier from overtaking an already-dispatched required writer.
func (m *Manager) admissionLockFor(scheduleID int64) *sync.Mutex {
	lock := m.admissionLocks[scheduleID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.admissionLocks[scheduleID] = lock
	}
	return lock
}

// commitAdmission is the linearization point between run admission and owner
// handoff/app deletion. WaitGroup.Add and active registration happen while the
// same mutex guards draining, so InterruptAndDrain can never observe zero and
// return before a pre-admitted goroutine registers itself.
func (m *Manager) commitAdmission(runID, appID int64, cancel context.CancelFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || m.draining || m.blockedApps[appID] {
		return ErrManagerStopped
	}
	m.active[runID] = cancel
	m.activeApps[runID] = appID
	if m.appActive[appID] == 0 {
		m.appDrained[appID] = make(chan struct{})
	}
	m.appActive[appID]++
	m.wg.Add(1)
	return nil
}

// unregisterActive removes a completed run from the active set.
func (m *Manager) unregisterActive(runID int64) {
	m.mu.Lock()
	appID := m.activeApps[runID]
	delete(m.active, runID)
	delete(m.activeApps, runID)
	delete(m.operatorCancelled, runID)
	if appID != 0 && m.appActive[appID] > 0 {
		m.appActive[appID]--
		if m.appActive[appID] == 0 {
			delete(m.appActive, appID)
			if drained := m.appDrained[appID]; drained != nil {
				close(drained)
				delete(m.appDrained, appID)
			}
		}
	}
	m.mu.Unlock()
}

// BlockAndDrainApp permanently closes admissions for an app being deleted,
// cancels its queued/running work, and waits for durable terminalization. A
// recreated slug receives a different app ID and is unaffected.
func (m *Manager) BlockAndDrainApp(ctx context.Context, appID int64) error {
	m.mu.Lock()
	m.blockedApps[appID] = true
	cancels := make([]context.CancelFunc, 0)
	for runID, cancel := range m.active {
		if m.activeApps[runID] == appID {
			cancels = append(cancels, cancel)
		}
	}
	drained := m.appDrained[appID]
	active := m.appActive[appID]
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if active == 0 || drained == nil {
		return nil
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel signals an in-flight run to terminate. Returns nil even when the run
// has already finished — cancellation is best-effort. The run id is recorded
// as operator-cancelled so it finalises as "cancelled" (terminal) rather than
// "interrupted" (re-fired by the startup reconcile) even if a service shutdown
// cancels it moments later.
func (m *Manager) Cancel(runID int64) error {
	m.mu.Lock()
	cancel, ok := m.active[runID]
	if ok {
		m.operatorCancelled[runID] = true
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// cancelStatus classifies why an in-flight run's context was cancelled. An
// operator Cancel recorded the run id, yielding "cancelled" (terminal). A
// cancel during Stop (service shutdown) with no operator record yields
// "interrupted", which startup reconciliation may re-fire for deploy-triggered
// work. Any other cancellation defaults to "cancelled".
func (m *Manager) cancelStatus(runID int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.operatorCancelled[runID] {
		return "cancelled"
	}
	if m.stopped || m.draining {
		return "interrupted"
	}
	return "cancelled"
}

// InterruptAndDrain is the owner-handoff counterpart to Stop. It rejects new
// admissions, cancels every active run, and waits for durable terminalization.
// Admissions intentionally remain closed after return: a retiring owner may
// still have pre-admitted HTTP handlers. A later successful owner startup must
// call ResumeAdmissions explicitly after completing recovery fences.
func (m *Manager) InterruptAndDrain(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return ErrManagerStopped
	}
	m.draining = true
	cancels := make([]context.CancelFunc, 0, len(m.active))
	for _, cancel := range m.active {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResumeAdmissions reopens a manager after a completed ownership-startup
// recovery. It is a no-op for a permanently stopped manager.
func (m *Manager) ResumeAdmissions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stopped {
		m.draining = false
	}
}

// Stop signals all in-flight runs to cancel and waits for their goroutines
// to finalize (update the schedule_runs row) or for ctx to expire. After
// Stop returns, Run rejects new admissions with ErrManagerStopped. ctx
// bounds how long shutdown waits; on expiry the still-running goroutines
// are abandoned (their next startup reconciliation marks them interrupted).
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	cancels := make([]context.CancelFunc, 0, len(m.active))
	for _, c := range m.active {
		cancels = append(cancels, c)
	}
	m.mu.Unlock()

	for _, c := range cancels {
		c()
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		m.terminalCancel()
	case <-ctx.Done():
		m.terminalCancel()
		slog.Warn("jobs: shutdown timeout; abandoning in-flight runs")
	}
}

// Run executes a scheduled run for the given schedule ID. It enforces the
// schedule's overlap policy and returns the run row ID even when a run is
// skipped. The actual command execution happens in a background goroutine;
// Run returns as soon as the run row is inserted (or the skipped row is
// finished).
func (m *Manager) Run(scheduleID int64, trigger string, userID *int64) (int64, error) {
	m.mu.Lock()
	stopped := m.stopped || m.draining
	admission := m.admissionLockFor(scheduleID)
	gate := m.producerGateFor(scheduleID)
	m.mu.Unlock()
	if stopped {
		return 0, ErrManagerStopped
	}

	// Enter the read fence before loading any mutable declaration or deployment
	// state. Otherwise a caller can snapshot command A, wait behind a deployment
	// that publishes command B, and then execute stale A against B's bundle.
	admission.Lock()
	gate.RLock()
	admission.Unlock()
	sched, err := m.store.GetSchedule(scheduleID)
	if err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("get schedule %d: %w", scheduleID, err)
	}
	sched, err = m.asExplicitRepairPublisher(sched, trigger)
	if err != nil {
		gate.RUnlock()
		return 0, err
	}
	app, err := m.store.GetAppByID(sched.AppID)
	if err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("get app %d: %w", sched.AppID, err)
	}
	if quarantined, err := m.store.AppCompatibilityQuarantined(app.ID); err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("check compatibility quarantine for app %d: %w", app.ID, err)
	} else if quarantined && !schedulePublishesData(sched, trigger) {
		gate.RUnlock()
		return 0, ErrAppCompatibilityQuarantined
	}
	// Hold a read admission fence from the pending-deployment check through
	// transfer to the launched run. A deploy's exclusive producer barrier can
	// therefore never slip between "no pending deployment" and actual run
	// ownership, leaving an old-bundle writer queued behind the new producer.
	if pending, err := m.store.HasPendingDeployment(app.ID); err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("check pending deployment for app %d: %w", app.ID, err)
	} else if pending {
		gate.RUnlock()
		return 0, ErrAppDeploying
	}
	deployments, err := m.store.ListDeployments(app.ID)
	if err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("list deployments for app %d: %w", app.ID, err)
	}
	if len(deployments) == 0 {
		gate.RUnlock()
		return 0, fmt.Errorf("app %q has no deployments; cannot run schedule", app.Slug)
	}
	return m.runForDeployment(sched, app, deployments[0], trigger, userID, gate)
}

// RunForDeployment admits a schedule run against one exact immutable bundle.
// Deploy convergence uses this path so a queued/asynchronous run cannot drift
// to a newer bundle between admission and execution.
func (m *Manager) RunForDeployment(scheduleID int64, trigger string, userID *int64, deployment *db.Deployment) (int64, error) {
	if deployment == nil {
		return 0, errors.New("schedule run deployment is required")
	}
	m.mu.Lock()
	stopped := m.stopped || m.draining
	admission := m.admissionLockFor(scheduleID)
	gate := m.producerGateFor(scheduleID)
	m.mu.Unlock()
	if stopped {
		return 0, ErrManagerStopped
	}
	admission.Lock()
	gate.RLock()
	admission.Unlock()
	sched, err := m.store.GetSchedule(scheduleID)
	if err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("get schedule %d: %w", scheduleID, err)
	}
	sched, err = m.asExplicitRepairPublisher(sched, trigger)
	if err != nil {
		gate.RUnlock()
		return 0, err
	}
	app, err := m.store.GetAppByID(sched.AppID)
	if err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("get app %d: %w", sched.AppID, err)
	}
	if quarantined, err := m.store.AppCompatibilityQuarantined(app.ID); err != nil {
		gate.RUnlock()
		return 0, fmt.Errorf("check compatibility quarantine for app %d: %w", app.ID, err)
	} else if quarantined && !schedulePublishesData(sched, trigger) {
		gate.RUnlock()
		return 0, ErrAppCompatibilityQuarantined
	}
	if deployment.AppID != app.ID {
		gate.RUnlock()
		return 0, fmt.Errorf("deployment %d belongs to app %d, want %d", deployment.ID, deployment.AppID, app.ID)
	}
	return m.runForDeployment(sched, app, deployment, trigger, userID, gate)
}

// RunDeployObligation executes a durable deploy convergence obligation. It
// always serializes behind existing work for the schedule and never applies
// overlap=skip: required desired-state work may wait, but it may not vanish.
func (m *Manager) RunDeployObligation(obligation *db.ScheduleDeployObligation) (int64, error) {
	if obligation == nil {
		return 0, errors.New("deploy obligation is required")
	}
	m.mu.Lock()
	stopped := m.stopped || m.draining
	m.mu.Unlock()
	if stopped {
		return 0, ErrManagerStopped
	}
	sched, err := m.store.GetSchedule(obligation.ScheduleID)
	if err != nil {
		return 0, fmt.Errorf("get schedule %d: %w", obligation.ScheduleID, err)
	}
	app, err := m.store.GetAppByID(sched.AppID)
	if err != nil {
		return 0, fmt.Errorf("get app %d: %w", sched.AppID, err)
	}
	deployments, err := m.store.ListDeployments(app.ID)
	if err != nil {
		return 0, fmt.Errorf("list deployments for app %d: %w", app.ID, err)
	}
	var deployment *db.Deployment
	for _, candidate := range deployments {
		if candidate.ID == obligation.DeploymentID {
			deployment = candidate
			break
		}
	}
	if deployment == nil {
		return 0, fmt.Errorf("deployment %d for obligation %d is unavailable", obligation.DeploymentID, obligation.ID)
	}
	// Execute the immutable obligation snapshot even if the schedule is edited
	// after admission. A subsequent reconciliation creates a new identity.
	snapshot := *sched
	snapshot.CommandJSON = obligation.ProducerCommandJSON
	snapshot.TimeoutSeconds = obligation.TimeoutSeconds
	snapshot.OnSuccess = obligation.OnSuccess
	snapshot.MinRollIntervalSeconds = obligation.MinRollIntervalSeconds
	snapshot.RollFallback = obligation.RollFallback
	snapshot.MaxDeferAgeSeconds = obligation.MaxDeferAgeSeconds
	return m.runRequired(&snapshot, app, deployment, obligation)
}

func (m *Manager) runRequired(sched *db.Schedule, app *db.App, deployment *db.Deployment, obligation *db.ScheduleDeployObligation) (int64, error) {
	m.mu.Lock()
	gate := m.producerGateFor(sched.ID)
	admission := m.admissionLockFor(sched.ID)
	m.mu.Unlock()

	// Reserve writer admission synchronously, but acquire the potentially
	// long-held writer gate in the run goroutine. The durable dispatcher stays
	// non-blocking while later readers/barriers still cannot overtake this run.
	admission.Lock()
	if pending, err := m.store.HasPendingDeployment(app.ID); err != nil {
		admission.Unlock()
		return 0, fmt.Errorf("check pending deployment for app %d: %w", app.ID, err)
	} else if pending {
		admission.Unlock()
		return 0, ErrAppDeploying
	}
	current, err := m.store.ListDeployments(app.ID)
	if err != nil {
		admission.Unlock()
		return 0, fmt.Errorf("revalidate deployment for app %d: %w", app.ID, err)
	}
	if len(current) == 0 || current[0].ID != deployment.ID {
		admission.Unlock()
		return 0, fmt.Errorf("deployment %d is no longer current", deployment.ID)
	}
	runID, err := m.insertDeployRunRow(sched, deployment, obligation)
	if err != nil {
		admission.Unlock()
		return 0, err
	}
	ctx, cancel := m.buildRunContext()
	if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
		cancel()
		admission.Unlock()
		m.finishRun(sched, runID, "interrupted", nil, "deploy", nil, false)
		return runID, err
	}
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer m.unregisterActive(runID)
		gate.Lock()
		admission.Unlock()
		defer gate.Unlock()
		// BeginDeployment is recorded before its producer gates are acquired.
		// Recheck after exclusive admission so a newly pending deployment cannot
		// make this immutable old-bundle writer run inside a deploy barrier.
		if pending, err := m.store.HasPendingDeployment(app.ID); err != nil || pending {
			m.finishRun(sched, runID, "failed", nil, "deploy", nil, false)
			return
		}
		current, err := m.store.ListDeployments(app.ID)
		if err != nil || len(current) == 0 || current[0].ID != deployment.ID {
			m.finishRun(sched, runID, "failed", nil, "deploy", nil, false)
			return
		}
		if ctx.Err() != nil {
			m.finishRun(sched, runID, m.cancelStatus(runID), nil, "deploy", nil, false)
			return
		}
		m.execute(ctx, sched, app, deployment, runID, "deploy", nil, false)
	}()
	return runID, nil
}

func (m *Manager) runForDeployment(sched *db.Schedule, app *db.App, deployment *db.Deployment, trigger string, userID *int64, gate *sync.RWMutex) (int64, error) {
	switch sched.OverlapPolicy {
	case "skip":
		return m.runWithSkip(sched, app, deployment, trigger, userID, gate)
	case "queue":
		return m.runWithQueue(sched, app, deployment, trigger, userID, gate)
	case "concurrent":
		return m.runConcurrent(sched, app, deployment, trigger, userID, gate)
	default:
		gate.RUnlock()
		return 0, fmt.Errorf("unknown overlap_policy %q", sched.OverlapPolicy)
	}
}

// runWithSkip launches a run only if no other run for this schedule is active.
// If one is already running, it records a skipped_overlap row and returns.
func (m *Manager) runWithSkip(sched *db.Schedule, app *db.App, deployment *db.Deployment, trigger string, userID *int64, gate *sync.RWMutex) (int64, error) {
	m.mu.Lock()
	slot := m.lockFor(sched.ID)
	acquired := slot.tryLock()
	m.mu.Unlock()

	if !acquired {
		gate.RUnlock()
		return m.recordSkipped(sched, deployment, trigger, userID)
	}

	runID, err := m.insertRunRow(sched, deployment, trigger, userID)
	if err != nil {
		slot.unlock()
		gate.RUnlock()
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
		cancel()
		slot.unlock()
		gate.RUnlock()
		m.finishRun(sched, runID, "interrupted", nil, trigger, userID, false)
		return runID, err
	}
	go func() {
		defer m.wg.Done()
		defer slot.unlock()
		defer gate.RUnlock()
		defer cancel()
		defer m.unregisterActive(runID)
		m.executeOrdinary(ctx, sched, app, deployment, runID, trigger, userID)
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
func (m *Manager) runWithQueue(sched *db.Schedule, app *db.App, deployment *db.Deployment, trigger string, userID *int64, gate *sync.RWMutex) (int64, error) {
	m.mu.Lock()
	sem := m.queueChan(sched.ID)
	slot := m.lockFor(sched.ID)
	m.mu.Unlock()

	// Fast path: if the slot is free, become the active run synchronously
	// here in the caller's goroutine. This locks in admission order before
	// any goroutine is launched.
	if slot.tryLock() {
		runID, err := m.insertRunRow(sched, deployment, trigger, userID)
		if err != nil {
			slot.unlock()
			gate.RUnlock()
			return 0, err
		}
		ctx, cancel := m.buildRunContext()
		if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
			cancel()
			slot.unlock()
			gate.RUnlock()
			m.finishRun(sched, runID, "interrupted", nil, trigger, userID, false)
			return runID, err
		}
		go func() {
			defer m.wg.Done()
			defer slot.unlock()
			defer gate.RUnlock()
			defer cancel()
			defer m.unregisterActive(runID)
			m.executeOrdinary(ctx, sched, app, deployment, runID, trigger, userID)
		}()
		return runID, nil
	}

	// Slot is held by an active run. Try to take the single queue slot.
	select {
	case sem <- struct{}{}:
	default:
		gate.RUnlock()
		return m.recordSkipped(sched, deployment, trigger, userID)
	}

	runID, err := m.insertRunRow(sched, deployment, trigger, userID)
	if err != nil {
		<-sem
		gate.RUnlock()
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
		cancel()
		<-sem
		gate.RUnlock()
		m.finishRun(sched, runID, "interrupted", nil, trigger, userID, false)
		return runID, err
	}
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer m.unregisterActive(runID)
		defer gate.RUnlock()

		if !slot.lock(ctx) {
			// Cancel arrived while waiting in the queue. Free the
			// queue slot first so a new admission can take our place,
			// then finalise as cancelled. We never acquired the slot,
			// so do not call slot.unlock.
			<-sem
			m.finishRun(sched, runID, m.cancelStatus(runID), nil, trigger, userID, false)
			return
		}
		// Promoted from queued to active. Free the queue slot now —
		// not at goroutine exit — so one new run can wait behind us
		// during execution. Holding sem through execute would collapse
		// the queue policy to "skip" (one active, no queued).
		<-sem
		defer slot.unlock()
		m.executeOrdinary(ctx, sched, app, deployment, runID, trigger, userID)
	}()

	return runID, nil
}

// runConcurrent allows ordinary runs to overlap one another; the shared
// producer gate still excludes required convergence work.
func (m *Manager) runConcurrent(sched *db.Schedule, app *db.App, deployment *db.Deployment, trigger string, userID *int64, gate *sync.RWMutex) (int64, error) {
	runID, err := m.insertRunRow(sched, deployment, trigger, userID)
	if err != nil {
		gate.RUnlock()
		return 0, err
	}

	ctx, cancel := m.buildRunContext()
	if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
		cancel()
		gate.RUnlock()
		m.finishRun(sched, runID, "interrupted", nil, trigger, userID, false)
		return runID, err
	}
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer m.unregisterActive(runID)
		defer gate.RUnlock()
		m.executeOrdinary(ctx, sched, app, deployment, runID, trigger, userID)
	}()

	return runID, nil
}

// executeOrdinary runs after admission synchronously transferred a producer
// read gate to its goroutine. Required convergence takes the exclusive side.
func (m *Manager) executeOrdinary(ctx context.Context, sched *db.Schedule, app *db.App, deployment *db.Deployment, runID int64, trigger string, userID *int64) {
	if ctx.Err() != nil {
		m.finishRun(sched, runID, m.cancelStatus(runID), nil, trigger, userID, false)
		return
	}
	m.execute(ctx, sched, app, deployment, runID, trigger, userID, false)
}

// producerGateFor must be called with m.mu held.
func (m *Manager) producerGateFor(scheduleID int64) *sync.RWMutex {
	gate := m.producerGates[scheduleID]
	if gate == nil {
		gate = &sync.RWMutex{}
		m.producerGates[scheduleID] = gate
	}
	return gate
}

// publicationGateFor must be called with m.mu held.
func (m *Manager) publicationGateFor(appID int64) *sync.RWMutex {
	gate := m.publicationGates[appID]
	if gate == nil {
		gate = &sync.RWMutex{}
		m.publicationGates[appID] = gate
	}
	return gate
}

// AcquireConsumerBootGate prevents a designated producer from publishing while
// a fresh consumer starts, reads startup data, and persists the generation it
// actually loaded. Callers must hold the returned release through UpsertReplica.
func (m *Manager) AcquireConsumerBootGate(appID int64) (func(), error) {
	m.mu.Lock()
	gate := m.publicationGateFor(appID)
	m.mu.Unlock()
	gate.RLock()
	releaseFile, _, err := m.acquirePublicationFileLock(appID, unix.LOCK_SH)
	if err != nil {
		gate.RUnlock()
		return nil, err
	}
	return func() {
		releaseFile()
		gate.RUnlock()
	}, nil
}

// AcquireCompatibleConsumerBootGate atomically couples publication exclusion
// with the durable compatibility check ordinary consumer starts require. The
// check runs after both the in-process and cross-process read fences are held,
// so a writer cannot fail and create uncertainty between check and boot.
func (m *Manager) AcquireCompatibleConsumerBootGate(appID int64) (func(), error) {
	release, err := m.AcquireConsumerBootGate(appID)
	if err != nil {
		return nil, err
	}
	quarantined, err := m.store.AppCompatibilityQuarantined(appID)
	if err != nil {
		release()
		return nil, fmt.Errorf("check compatibility under consumer publication fence: %w", err)
	}
	if quarantined {
		release()
		return nil, ErrAppCompatibilityQuarantined
	}
	return release, nil
}

func (m *Manager) acquirePublicationWriter(appID int64) (func(), *os.File, error) {
	m.mu.Lock()
	gate := m.publicationGateFor(appID)
	m.mu.Unlock()
	gate.Lock()
	releaseFile, file, err := m.acquirePublicationFileLock(appID, unix.LOCK_EX)
	if err != nil {
		gate.Unlock()
		return nil, nil, err
	}
	return func() {
		releaseFile()
		gate.Unlock()
	}, file, nil
}

func (m *Manager) acquireJobExecution(ctx context.Context, appID int64) (func(), *os.File, error) {
	return m.acquireJobExecutionFileLockContext(ctx, appID, unix.LOCK_SH)
}

// AcquireConsumerLifetime is wired into process.Manager.Start. A native
// consumer inherits the shared descriptor, so even an untracked survivor
// remains visible to the exclusive candidate-producer fence.
func (m *Manager) AcquireConsumerLifetime(appID int64) (func(), *os.File, error) {
	return m.acquireConsumerLifetimeFileLockContext(context.Background(), appID, unix.LOCK_SH)
}

func (m *Manager) AcquireExclusiveConsumerLifetime(ctx context.Context, appID int64) (func(), error) {
	release, _, err := m.acquireConsumerLifetimeFileLockContext(ctx, appID, unix.LOCK_EX)
	return release, err
}

// AcquirePublicationRecoveryFences takes an exclusive physical-writer fence
// for every app in stable order. A successor owner holds these while it
// terminalizes inherited running rows and decides which consumers are safe to
// recover. The context-aware file acquisition lets a process that loses the
// lease while waiting abandon startup cleanly.
func (m *Manager) AcquirePublicationRecoveryFences(ctx context.Context, appIDs []int64) (func(), error) {
	ids := append([]int64(nil), appIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	releases := make([]func(), 0, len(unique))
	for _, appID := range unique {
		releaseExecution, _, err := m.acquireJobExecutionFileLockContext(ctx, appID, unix.LOCK_EX)
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		m.mu.Lock()
		gate := m.publicationGateFor(appID)
		m.mu.Unlock()
		gate.Lock()
		releaseFile, _, err := m.acquirePublicationFileLockContext(ctx, appID, unix.LOCK_EX)
		if err != nil {
			gate.Unlock()
			releaseExecution()
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, func() {
			releaseFile()
			gate.Unlock()
			releaseExecution()
		})
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}, nil
}

// acquirePublicationFileLock extends the in-process RW fence across ZDT/HA
// processes on the same data filesystem. A distinct descriptor per acquisition
// is required: flock state is associated with an open file description, so
// sharing one descriptor would let concurrent readers accidentally convert it.
func (m *Manager) acquirePublicationFileLock(appID int64, mode int) (func(), *os.File, error) {
	return m.acquirePublicationFileLockContext(context.Background(), appID, mode)
}

func (m *Manager) acquirePublicationFileLockContext(ctx context.Context, appID int64, mode int) (func(), *os.File, error) {
	path := filepath.Join(m.publicationLockDir, fmt.Sprintf("app-%d.publication.lock", appID))
	return acquireFileLockContext(ctx, path, mode, fmt.Sprintf("app %d publication", appID))
}

func (m *Manager) acquireJobExecutionFileLockContext(ctx context.Context, appID int64, mode int) (func(), *os.File, error) {
	path := filepath.Join(m.publicationLockDir, fmt.Sprintf("app-%d.jobs.lock", appID))
	return acquireFileLockContext(ctx, path, mode, fmt.Sprintf("app %d schedule execution", appID))
}

func (m *Manager) acquireConsumerLifetimeFileLockContext(ctx context.Context, appID int64, mode int) (func(), *os.File, error) {
	path := filepath.Join(m.publicationLockDir, fmt.Sprintf("app-%d.consumers.lock", appID))
	return acquireFileLockContext(ctx, path, mode, fmt.Sprintf("app %d consumer lifetime", appID))
}

func acquireFileLockContext(ctx context.Context, path string, mode int, label string) (func(), *os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s lock: %w", label, err)
	}
	for {
		err = unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, nil, fmt.Errorf("acquire %s lock: %w", label, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, nil, fmt.Errorf("acquire %s lock: %w", label, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Close rather than LOCK_UN. A native producer inherits this same open
			// file description; an explicit unlock would drop the fence for every
			// inherited reference, including a still-running background writer.
			// Closing our reference leaves flock held until the final descendant
			// closes its copy.
			_ = f.Close()
		})
	}, f, nil
}

func schedulePublishesData(sched *db.Schedule, trigger string) bool {
	return trigger == "deploy" || sched.DeployTrigger != schedulespec.DeployTriggerNever || sched.OnSuccess == "roll"
}

// asExplicitRepairPublisher makes a manual run the authoritative fenced writer
// when this exact schedule owns durable write uncertainty. This is independent
// of its declaration because legacy schedules had no producer policy to
// preserve; without this narrow promotion, the fail-closed upgrade fence would
// be impossible to repair for an on_success=none/deploy_trigger=never job.
func (m *Manager) asExplicitRepairPublisher(sched *db.Schedule, trigger string) (*db.Schedule, error) {
	if trigger != "manual" || schedulePublishesData(sched, trigger) {
		return sched, nil
	}
	repair, err := m.store.ScheduleProducerRepairRequired(sched.ID)
	if err != nil {
		return nil, fmt.Errorf("check schedule %d producer repair: %w", sched.ID, err)
	}
	if !repair {
		return sched, nil
	}
	snapshot := *sched
	// The execution-only policy marker feeds every publication/fencing decision
	// without mutating the persisted declaration or requesting an activation.
	snapshot.DeployTrigger = schedulespec.DeployTriggerBundleChange
	return &snapshot, nil
}

// AcquireProducerGates excludes every ordinary or convergence execution for
// the supplied schedules. Deployment uses this barrier while it prepares and
// publishes candidate-bundle data before any candidate consumer starts. IDs
// are sorted and deduplicated so two callers can never deadlock by choosing a
// different lock order.
func (m *Manager) AcquireProducerGates(scheduleIDs []int64) func() {
	ids := append([]int64(nil), scheduleIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	gates := make([]*sync.RWMutex, 0, len(unique))
	admissions := make([]*sync.Mutex, 0, len(unique))
	m.mu.Lock()
	for _, id := range unique {
		admissions = append(admissions, m.admissionLockFor(id))
		gates = append(gates, m.producerGateFor(id))
	}
	m.mu.Unlock()
	for _, admission := range admissions {
		admission.Lock()
	}
	for _, gate := range gates {
		gate.Lock()
	}
	for i := len(admissions) - 1; i >= 0; i-- {
		admissions[i].Unlock()
	}
	return func() {
		for i := len(gates) - 1; i >= 0; i-- {
			gates[i].Unlock()
		}
	}
}

// RunCandidateProducerLocked synchronously executes one immutable candidate
// producer while its gate is held by AcquireProducerGates. Completion and
// authoritative producer provenance are durable before this method returns,
// making it suitable as a pre-start deployment barrier.
func (m *Manager) RunCandidateProducerLocked(sched *db.Schedule, app *db.App, deployment *db.Deployment) (int64, error) {
	if sched == nil || app == nil || deployment == nil {
		return 0, errors.New("candidate producer requires schedule, app, and deployment")
	}
	if sched.AppID != app.ID || deployment.AppID != app.ID {
		return 0, errors.New("candidate producer app identity mismatch")
	}
	m.mu.Lock()
	unavailable := m.stopped || m.draining
	m.mu.Unlock()
	if unavailable {
		return 0, ErrManagerStopped
	}
	snapshot := *sched
	// Serving activation is unnecessary and unsafe before the candidate app is
	// live. The normal policy remains on the persisted declaration for later
	// cron/manual runs.
	snapshot.OnSuccess = "none"
	runID, err := m.insertRunRow(&snapshot, deployment, "deploy", nil)
	if err != nil {
		return 0, err
	}
	ctx, cancel := m.buildRunContext()
	if err := m.commitAdmission(runID, app.ID, cancel); err != nil {
		cancel()
		m.finishRun(&snapshot, runID, "interrupted", nil, "deploy", nil, false)
		return runID, err
	}
	defer m.wg.Done()
	defer cancel()
	defer m.unregisterActive(runID)
	m.execute(ctx, &snapshot, app, deployment, runID, "deploy", nil, true)
	run, err := m.store.GetScheduleRun(runID)
	if err != nil {
		return runID, fmt.Errorf("read candidate producer result: %w", err)
	}
	if run.Status != "succeeded" {
		return runID, fmt.Errorf("candidate producer run %d finished %s", runID, run.Status)
	}
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
func (m *Manager) insertRunRow(sched *db.Schedule, deployment *db.Deployment, trigger string, userID *int64) (int64, error) {
	canonical, fingerprint, err := schedulespec.ProducerIdentity(sched.CommandJSON)
	if err != nil {
		return 0, err
	}
	deploymentID := deployment.ID
	return m.store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID:             sched.ID,
		Status:                 "running",
		Trigger:                trigger,
		TriggeredByUserID:      userID,
		StartedAt:              time.Now().UTC(),
		LogPath:                "", // updated after log file creation
		OnSuccess:              sched.OnSuccess,
		MinRollIntervalSeconds: sched.MinRollIntervalSeconds,
		RollFallback:           sched.RollFallback,
		MaxDeferAgeSeconds:     sched.MaxDeferAgeSeconds,
		DeploymentID:           &deploymentID,
		AppVersion:             deployment.Version,
		ContentDigest:          deployment.ContentDigest,
		ProducerFingerprint:    fingerprint,
		ProducerCommandJSON:    canonical,
		PublishesData:          schedulePublishesData(sched, trigger),
	})
}

func (m *Manager) insertDeployRunRow(sched *db.Schedule, deployment *db.Deployment, obligation *db.ScheduleDeployObligation) (int64, error) {
	deploymentID := deployment.ID
	obligationID := obligation.ID
	return m.store.InsertDeployScheduleRun(db.InsertScheduleRunParams{
		ScheduleID:             sched.ID,
		Status:                 "running",
		Trigger:                "deploy",
		StartedAt:              time.Now().UTC(),
		OnSuccess:              obligation.OnSuccess,
		MinRollIntervalSeconds: obligation.MinRollIntervalSeconds,
		RollFallback:           obligation.RollFallback,
		MaxDeferAgeSeconds:     obligation.MaxDeferAgeSeconds,
		DeploymentID:           &deploymentID,
		AppVersion:             deployment.Version,
		ContentDigest:          deployment.ContentDigest,
		ProducerFingerprint:    obligation.ProducerFingerprint,
		ProducerCommandJSON:    obligation.ProducerCommandJSON,
		PublishesData:          true,
		DeployObligationID:     &obligationID,
	})
}

// recordSkipped inserts a run row and immediately finishes it with
// status "skipped_overlap". Returns the run ID.

func (m *Manager) recordSkipped(sched *db.Schedule, deployment *db.Deployment, trigger string, userID *int64) (int64, error) {
	canonical, fingerprint, identityErr := schedulespec.ProducerIdentity(sched.CommandJSON)
	if identityErr != nil {
		return 0, identityErr
	}
	deploymentID := deployment.ID
	runID, err := m.store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID:             sched.ID,
		Status:                 "skipped_overlap",
		Trigger:                trigger,
		TriggeredByUserID:      userID,
		StartedAt:              time.Now().UTC(),
		OnSuccess:              sched.OnSuccess,
		MinRollIntervalSeconds: sched.MinRollIntervalSeconds,
		RollFallback:           sched.RollFallback,
		MaxDeferAgeSeconds:     sched.MaxDeferAgeSeconds,
		DeploymentID:           &deploymentID,
		AppVersion:             deployment.Version,
		ContentDigest:          deployment.ContentDigest,
		ProducerFingerprint:    fingerprint,
		ProducerCommandJSON:    canonical,
		PublishesData:          schedulePublishesData(sched, trigger),
	})
	if err != nil {
		return 0, fmt.Errorf("insert skipped run: %w", err)
	}
	if err := m.store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID:  runID,
		Status: "skipped_overlap",
		// No process ran, so no exit code was observed.
		ExitCode:   nil,
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
func (m *Manager) execute(ctx context.Context, sched *db.Schedule, app *db.App, deployment *db.Deployment, runID int64, trigger string, userID *int64, candidate bool) {
	// Global concurrency gate. Wait here (not before goroutine launch) so the
	// per-run timeout below still starts only once the run actually runs. A
	// Cancel() while queued for a slot finalises the run as cancelled instead
	// of hanging.
	select {
	case m.globalSem <- struct{}{}:
		defer func() { <-m.globalSem }()
	case <-ctx.Done():
		m.finishRun(sched, runID, m.cancelStatus(runID), nil, trigger, userID, false)
		return
	}
	releaseExecution, executionLifetimeFile, err := m.acquireJobExecution(ctx, app.ID)
	if err != nil {
		slog.Error("schedule run: acquire physical execution fence", "schedule", sched.ID, "run", runID, "err", err)
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}
	defer releaseExecution()
	if ctx.Err() != nil {
		m.finishRun(sched, runID, m.cancelStatus(runID), nil, trigger, userID, false)
		return
	}
	if run, err := m.store.GetScheduleRun(runID); err != nil || run.Status != "running" {
		m.finishRun(sched, runID, "superseded", nil, trigger, userID, false)
		return
	}
	if !candidate && !m.servingRunStillCurrent(sched, app, deployment, runID, trigger) {
		m.finishRun(sched, runID, "superseded", nil, trigger, userID, false)
		return
	}
	var publicationLifetimeFile, candidateConsumerFenceFile *os.File
	if schedulePublishesData(sched, trigger) {
		releasePublication, lifetimeFile, err := m.acquirePublicationWriter(app.ID)
		if err != nil {
			slog.Error("schedule run: acquire publication fence", "schedule", sched.ID, "run", runID, "err", err)
			m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
			return
		}
		defer releasePublication()
		publicationLifetimeFile = lifetimeFile
		if candidate {
			releaseConsumers, consumerFile, err := m.acquireConsumerLifetimeFileLockContext(ctx, app.ID, unix.LOCK_EX)
			if err != nil {
				slog.Error("schedule run: acquire candidate consumer lifetime fence", "schedule", sched.ID, "run", runID, "err", err)
				m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
				return
			}
			defer releaseConsumers()
			candidateConsumerFenceFile = consumerFile
		}
		select {
		case <-ctx.Done():
			m.finishRun(sched, runID, m.cancelStatus(runID), nil, trigger, userID, false)
			return
		default:
		}
		// The writer may have been admitted by a retiring process before a new
		// deployment acquired the shared consumer fence, then waited behind that
		// consumer and reached this exclusive fence only after promotion. Validate
		// at the physical write boundary so an old bundle/command cannot overwrite
		// data after new consumers start. Candidate producers are the deliberate
		// exception: they execute the exact pending deployment while its lifecycle
		// handler owns the app operation fence.
		if !candidate && !m.servingRunStillCurrent(sched, app, deployment, runID, trigger) {
			m.finishRun(sched, runID, "superseded", nil, trigger, userID, false)
			return
		}
	}

	runCtx := ctx
	if sched.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(sched.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	// Build log file path and create directory.
	logDir := filepath.Join(m.appsDir, app.Slug, "schedules", fmt.Sprintf("%d", sched.ID))
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("run-%d.log", runID))

	logFile, err := os.Create(logPath)
	if err != nil {
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}
	defer logFile.Close()

	if err := m.store.SetScheduleRunLogPath(runID, logPath); err != nil {
		slog.Warn("set schedule run log path", "run", runID, "err", err)
		// Non-fatal — proceed with the run. The streaming endpoint will fall back gracefully.
	}

	// Build env vars, decrypting secrets. Secret values are partitioned into
	// secretEnv so the runtime can deliver them out of band; non-secret values
	// stay in env. Decryption fails closed (see appenv.Resolve): running the job
	// with a ciphertext or missing secret would silently misbehave instead of
	// surfacing a key/rotation problem, and app startup fails closed the same way.
	envVars, err := m.store.ListAppEnvVars(app.ID)
	if err != nil {
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}
	env, secretEnv, err := appenv.Resolve(envVars, m.secretsKey)
	if err != nil {
		fmt.Fprintf(logFile, "shinyhub: %v\n", err)
		slog.Error("schedule run: secret decrypt failed", "schedule", sched.ID, "run", runID, "err", err)
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}

	// Build shared mounts.
	mounts, err := m.store.ListSharedDataSources(app.ID)
	if err != nil {
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
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
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}

	// Build app data dir.
	appDataPath := filepath.Join(m.appDataDir, app.Slug)
	if err := os.MkdirAll(appDataPath, 0o750); err != nil {
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}

	bundleDir := deployment.BundleDir

	// Jobs run on the lowest-indexed placement tier so they execute on the
	// same runtime that serves replica 0 of the app. A single replica is
	// used as the fallback count so ExpandPlacement never errors on apps
	// with no explicit placement.
	assignments, err := process.ExpandPlacement(app.PlacementMap(), m.tierOrder, 1, m.defaultTier)
	if err != nil {
		fmt.Fprintf(logFile, "shinyhub: resolve job tier: %v\n", err)
		m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
		return
	}
	jobTier := m.defaultTier
	if len(assignments) > 0 {
		jobTier = assignments[0].Tier
	}
	rt := m.procMgr.RuntimeForTier(jobTier)
	if schedulePublishesData(sched, trigger) {
		isolation := app.WorkerIsolation
		if isolation == "" {
			isolation = m.defaultWorkerIsolation
		}
		if isolation == "" {
			isolation = "multiplex"
		}
		if isolation != "multiplex" {
			fmt.Fprintf(logFile, "shinyhub: data-producing schedules require worker_isolation=multiplex; effective mode is %q\n", isolation)
			m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
			return
		}
		inheritor, ok := rt.(process.LifetimeFileInheritor)
		if !ok || !inheritor.InheritsLifetimeFiles() {
			fmt.Fprintf(logFile, "shinyhub: data-producing schedules require a native runtime with inherited lifetime fences; tier %q is unsupported\n", jobTier)
			m.finishRun(sched, runID, "failed", nil, trigger, userID, false)
			return
		}
	}

	params := process.StartParams{
		Slug:          app.Slug,
		AppID:         app.ID,
		Dir:           bundleDir,
		Command:       cmd,
		Env:           env,
		SecretEnv:     secretEnv,
		Tier:          jobTier,
		AppDataPath:   appDataPath,
		SharedMounts:  sharedMounts,
		ContentDigest: deployment.ContentDigest,
		AppVersion:    deployment.Version,
		DeploymentID:  deployment.ID,
		// JobRunID namespaces this one-shot run's own cgroup so a capped job never
		// shares a live replica's. The resolved limits below mirror the per-replica
		// ceiling so a heavy refresh job cannot exceed the app's memory/CPU budget.
		JobRunID: runID,
	}
	if inheritor, ok := rt.(process.LifetimeFileInheritor); ok && inheritor.InheritsLifetimeFiles() {
		// Native descendants inherit the same open file description. If the
		// control-plane process is killed, flock therefore remains held until the
		// physical producer tree exits; a successor cannot boot a consumer over a
		// still-writing orphan. Other runtimes use their durable recovery fence.
		params.LifetimeFiles = append(params.LifetimeFiles, executionLifetimeFile)
		if publicationLifetimeFile != nil {
			params.LifetimeFiles = append(params.LifetimeFiles, publicationLifetimeFile)
		}
		if candidateConsumerFenceFile != nil {
			params.LifetimeFiles = append(params.LifetimeFiles, candidateConsumerFenceFile)
		}
	}
	if m.resolveResources != nil {
		params.MemoryLimitMB, params.CPUQuotaPercent = m.resolveResources(app)
	}

	// Remote tiers do not have host-side app data. Drop the host-only paths
	// so the remote runtime receives only the source slug and can pull data
	// through its own mechanism.
	if !rt.HostProvidesAppData() {
		params.AppDataPath = ""
		for i := range params.SharedMounts {
			params.SharedMounts[i].HostPath = ""
		}
	}

	// Run the command.
	var logWriter io.Writer = logFile
	info, runErr := rt.RunOnce(runCtx, params, logWriter)

	// A non-nil runErr means the runtime could not run the command (e.g. the
	// binary is not on PATH, so the process never started and emitted no output
	// of its own). Persist the error to the run log so `schedule logs --run N`
	// surfaces the cause instead of an empty file.
	if runErr != nil {
		fmt.Fprintf(logFile, "shinyhub: run failed: %v\n", runErr)
	}

	// Determine final status. exit_code is recorded only when the process ran
	// to completion and we observed its own exit code (a clean success, or a
	// non-zero exit). A run that never launched (runErr), was killed (timeout,
	// operator cancel, or service shutdown), or died of its own signal records
	// a NULL exit_code - status carries the reason in those cases.
	status := "succeeded"
	var exitCode *int
	switch {
	case runErr != nil:
		// The runtime could not launch the process; no exit was observed.
		status = "failed"
	case info.Signaled && runCtx.Err() == context.DeadlineExceeded:
		status = "timed_out"
	case info.Signaled && runCtx.Err() == context.Canceled:
		// Killed by an explicit cancel: an operator Cancel ("cancelled") or a
		// service-shutdown Stop ("interrupted").
		status = m.cancelStatus(runID)
	case info.Signaled:
		// Signaled without our context being cancelled - the process died of
		// its own signal (crash / external kill), which is a failure.
		status = "failed"
	case info.Code != 0:
		status = "failed"
		exitCode = intPtr(info.Code)
	default:
		// Exited 0 on its own.
		exitCode = intPtr(info.Code)
	}

	m.finishRun(sched, runID, status, exitCode, trigger, userID, schedulePublishesData(sched, trigger))
}

func (m *Manager) servingRunStillCurrent(sched *db.Schedule, app *db.App, deployment *db.Deployment, runID int64, trigger string) bool {
	run, err := m.store.GetScheduleRun(runID)
	if err != nil || run.Status != "running" {
		return false
	}
	if pending, err := m.store.HasPendingDeployment(app.ID); err != nil || pending {
		return false
	}
	current, err := m.store.ListDeployments(app.ID)
	if err != nil || len(current) == 0 || current[0].ID != deployment.ID {
		return false
	}
	declaration, err := m.store.GetSchedule(sched.ID)
	if err != nil || declaration.AppID != app.ID || (!declaration.Enabled && trigger != "manual") {
		return false
	}
	_, admittedFingerprint, err := schedulespec.ProducerIdentity(sched.CommandJSON)
	if err != nil {
		return false
	}
	_, currentFingerprint, err := schedulespec.ProducerIdentity(declaration.CommandJSON)
	return err == nil && currentFingerprint == admittedFingerprint
}

// intPtr returns a pointer to i, used to pass a concrete exit code where a
// nil pointer means "no observed exit" (an interrupted run).
func intPtr(i int) *int { return &i }

// finishRun updates the run row and logs an audit event. A nil exitCode is
// recorded as SQL NULL, used for a run that finished without observing a
// process exit (interrupted by a service restart).
func (m *Manager) finishRun(sched *db.Schedule, runID int64, status string, exitCode *int, trigger string, userID *int64, dataWriteAttempted bool) {
	finishedAt := time.Now().UTC()
	var activation *db.ScheduleActivation
	var err error
	retryDelay := 50 * time.Millisecond
	for {
		activation, err = m.store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
			RunID: runID, Status: status, ExitCode: exitCode, FinishedAt: finishedAt,
			DataWriteAttempted: dataWriteAttempted,
		})
		if err == nil {
			break
		}
		// App deletion cascades schedules and runs. A run row can be inserted
		// immediately before the app admission fence closes, then disappear before
		// commitAdmission rejects it. There is no terminal row left to persist and
		// retrying ErrNotFound would hang scheduler shutdown forever.
		if errors.Is(err, db.ErrNotFound) {
			slog.Info("schedule run: terminal target disappeared", "schedule_id", sched.ID, "run_id", runID)
			return
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-timer.C:
			if retryDelay < time.Second {
				retryDelay *= 2
				if retryDelay > time.Second {
					retryDelay = time.Second
				}
			}
		case <-m.terminalCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			slog.Error("schedule run: terminal persistence abandoned at shutdown",
				"schedule_id", sched.ID, "run_id", runID, "status", status, "error", err)
			return
		}
	}
	if dataWriteAttempted && status != "succeeded" && m.onUnsafePublication != nil {
		if app, appErr := m.store.GetAppByID(sched.AppID); appErr == nil {
			m.onUnsafePublication(app.ID, app.Slug, status)
		}
	}

	if m.metrics != nil {
		slug := ""
		if app, err := m.store.GetAppByID(sched.AppID); err == nil {
			slug = app.Slug
		}
		m.metrics.RecordScheduleRun(slug, sched.Name, status)
	}

	detail := fmt.Sprintf(`{"trigger":%q,"run_id":%d}`, trigger, runID)
	if activation != nil {
		detail = fmt.Sprintf(`{"trigger":%q,"run_id":%d,"activation_id":%d,"target_generation":%d}`,
			trigger, runID, activation.ID, activation.TargetGeneration)
	}
	m.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       "schedule_run_" + status,
		ResourceType: "schedule",
		ResourceID:   fmt.Sprintf("%d", sched.ID),
		Detail:       detail,
	})
}
