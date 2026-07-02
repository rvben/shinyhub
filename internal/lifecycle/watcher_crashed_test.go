package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// A runtime crash loop - the replica boots fine but keeps exiting shortly after
// (e.g. a lazy import that fails) - never trips the boot-failure budget, because
// every successful boot resets it. The crash-count guard catches it: after more
// crashes than the restart budget, the app is marked crashed with the reason.
func TestHandleCrashed_RuntimeCrashLoopMarksCrashed(t *testing.T) {
	app := &db.App{ID: 1, Slug: "loopy", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"loopy": app}, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/loopy"}})
	mgr := &fakeManager{logTail: "RuntimeError: simulated runtime failure"}
	boots := 0
	deployFn := func(_, _ string, idx int) (*deploy.Result, error) {
		boots++
		return &deploy.Result{Index: idx, PID: 100 + boots, Port: 20000 + boots}, nil
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 2}, mgr, newFakeProxy(), st, deployFn)

	for i := 0; i < 3; i++ { // RestartMaxAttempts (2) restarts, then give up on the 3rd crash
		w.handleCrashed("loopy", 0)
	}

	if got := appStatusOf(st, "loopy"); got != "crashed" {
		t.Fatalf("status = %q, want crashed after a runtime crash loop", got)
	}
	st.mu.Lock()
	reason := st.crashReasons["loopy"]
	st.mu.Unlock()
	if !strings.Contains(reason, "RuntimeError") {
		t.Fatalf("crash reason = %q, want the log tail", reason)
	}
	if boots != 2 {
		t.Fatalf("boots = %d, want 2 (restarts before giving up on the 3rd crash)", boots)
	}
}

// TestHandleCrashed_WritesAuditEvent verifies that when the watcher gives up on
// a crash-looping app it records an "app_crashed" audit event (system-generated,
// nil user) naming the app and the reason - so the audit log has a queryable
// record of the app going down, not just the app row's transient last_error.
func TestHandleCrashed_WritesAuditEvent(t *testing.T) {
	app := &db.App{ID: 1, Slug: "loopy", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"loopy": app}, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/loopy"}})
	mgr := &fakeManager{logTail: "RuntimeError: simulated runtime failure"}
	deployFn := func(_, _ string, idx int) (*deploy.Result, error) {
		return &deploy.Result{Index: idx, PID: 1, Port: 2}, nil
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 2}, mgr, newFakeProxy(), st, deployFn)

	for i := 0; i < 3; i++ {
		w.handleCrashed("loopy", 0)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	var crashEvent *db.AuditEventParams
	for i := range st.auditEvents {
		if st.auditEvents[i].Action == "app_crashed" && st.auditEvents[i].ResourceID == "loopy" {
			crashEvent = &st.auditEvents[i]
			break
		}
	}
	if crashEvent == nil {
		t.Fatalf("expected an app_crashed audit event for loopy, got %+v", st.auditEvents)
	}
	if crashEvent.UserID != nil {
		t.Errorf("crash audit event must be system-generated (nil user), got %v", *crashEvent.UserID)
	}
	if crashEvent.ResourceType != "app" {
		t.Errorf("resource_type = %q, want app", crashEvent.ResourceType)
	}
	if !strings.Contains(crashEvent.Detail, "RuntimeError") {
		t.Errorf("crash audit detail = %q, want it to carry the reason", crashEvent.Detail)
	}
	// Exactly one event even though handleCrashed ran three times (only the
	// give-up transition is an incident).
	n := 0
	for _, e := range st.auditEvents {
		if e.Action == "app_crashed" && e.ResourceID == "loopy" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one app_crashed event, got %d", n)
	}
}

// When the most recent exit was an OOM-kill (the replica exceeded its memory
// limit), the crash reason must NAME the limit rather than reading as a generic
// crash, so the operator can see it was the ceiling - not a code bug.
func TestHandleCrashed_OOMKillNamesMemoryLimit(t *testing.T) {
	app := &db.App{ID: 1, Slug: "hungry", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"hungry": app}, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/hungry"}})
	mgr := &fakeManager{
		logTail: "loading frame...",
		lastExit: map[replicaKey]process.ExitVerdict{
			{"hungry", 0}: {OOMKilled: true, MemoryLimitMB: 2048, At: time.Now()},
		},
	}
	deployFn := func(_, _ string, idx int) (*deploy.Result, error) {
		return &deploy.Result{Index: idx, PID: 1, Port: 2}, nil
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 2}, mgr, newFakeProxy(), st, deployFn)

	for i := 0; i < 3; i++ {
		w.handleCrashed("hungry", 0)
	}

	if got := appStatusOf(st, "hungry"); got != "crashed" {
		t.Fatalf("status = %q, want crashed", got)
	}
	st.mu.Lock()
	reason := st.crashReasons["hungry"]
	st.mu.Unlock()
	if !strings.Contains(reason, "memory limit") || !strings.Contains(reason, "2048") {
		t.Fatalf("crash reason = %q, want it to name the exceeded memory limit (2048)", reason)
	}
}

// Crashes spaced further apart than the loop window are separate incidents, not a
// loop: an app that crashes occasionally is never marked crashed.
func TestHandleCrashed_StableBetweenCrashesDoesNotLoop(t *testing.T) {
	app := &db.App{ID: 1, Slug: "occasional", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"occasional": app}, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/x"}})
	deployFn := func(_, _ string, idx int) (*deploy.Result, error) {
		return &deploy.Result{Index: idx, PID: 1, Port: 2}, nil
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 2}, &fakeManager{}, newFakeProxy(), st, deployFn)

	for i := 0; i < 4; i++ {
		w.handleCrashed("occasional", 0)
		// Backdate the crash so the next one starts a fresh incident.
		w.mu.Lock()
		w.lastCrash[replicaKey{"occasional", 0}] = time.Now().Add(-2 * crashLoopWindow)
		w.mu.Unlock()
	}

	if got := appStatusOf(st, "occasional"); got == "crashed" {
		t.Fatalf("status = crashed, but crashes were spaced beyond the window (must not loop)")
	}
}

func noopDeploy(_, _ string, idx int) (*deploy.Result, error) {
	return &deploy.Result{Index: idx}, nil
}

// When every replica is down and the restart budget is spent, the runtime status
// authority surfaces the app as "crashed" with the log-tail reason - not a vague
// "degraded" - so the dashboard can show why and offer Restart.
func TestReconcileAppStatus_GivesUpToCrashed(t *testing.T) {
	app := &db.App{ID: 1, Slug: "broke", Status: "degraded", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"broke": app}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "crashed", DesiredState: "running"}},
	}
	mgr := &fakeManager{logTail: "Traceback (most recent call last):\nKeyError: 'x'"}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st, noopDeploy)
	// The watchdog has spent the restart budget for the only slot.
	w.attempts[replicaKey{"broke", 0}] = 5

	w.reconcileAppStatus(app)

	if got := appStatusOf(st, "broke"); got != "crashed" {
		t.Fatalf("status = %q, want crashed (budget exhausted, 0 healthy)", got)
	}
	st.mu.Lock()
	reason := st.crashReasons["broke"]
	st.mu.Unlock()
	if !strings.Contains(reason, "KeyError") {
		t.Fatalf("crash reason = %q, want it to contain the log tail", reason)
	}
}

// While the restart budget remains, a fully-down app is still self-healing, so it
// stays "degraded" (the watchdog keeps retrying) rather than jumping to crashed.
func TestReconcileAppStatus_StillRetrying_StaysDegraded(t *testing.T) {
	app := &db.App{ID: 1, Slug: "flaky", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"flaky": app}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "crashed", DesiredState: "running"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, noopDeploy)
	w.attempts[replicaKey{"flaky", 0}] = 2 // budget remains

	w.reconcileAppStatus(app)

	if got := appStatusOf(st, "flaky"); got != "degraded" {
		t.Fatalf("status = %q, want degraded (still retrying, not yet crashed)", got)
	}
}

// A restart of a crash-looped app boots its replicas back to running; the status
// authority must then clear the spent budget and NOT immediately re-crash it (the
// "Restart button works" guarantee). Mirrors handleRestartApp, which marks the
// replicas running before flipping the app status.
func TestReconcileAppStatus_RestartedAppNotRecrashed(t *testing.T) {
	app := &db.App{ID: 1, Slug: "fixed", Status: "running", Replicas: 1}
	st := newFakeStore(map[string]*db.App{"fixed": app}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, noopDeploy)
	w.attempts[replicaKey{"fixed", 0}] = 5 // budget still maxed from the prior crash loop

	w.reconcileAppStatus(app)

	if got := appStatusOf(st, "fixed"); got != "running" {
		t.Fatalf("status = %q, want running (a booted replica must not be re-crashed)", got)
	}
	w.mu.Lock()
	_, stillThere := w.attempts[replicaKey{"fixed", 0}]
	w.mu.Unlock()
	if stillThere {
		t.Fatalf("restart budget not cleared on a fully-healthy app; a later crash would get no retries")
	}
}

// A partially-healthy app (some replicas serving) is degraded, never crashed,
// even when the down slots have exhausted their budget.
func TestReconcileAppStatus_PartialHealthy_StaysDegraded(t *testing.T) {
	app := &db.App{ID: 1, Slug: "half", Status: "running", Replicas: 2}
	st := newFakeStore(map[string]*db.App{"half": app}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "crashed", DesiredState: "running"},
		},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, noopDeploy)
	w.attempts[replicaKey{"half", 0}] = 5
	w.attempts[replicaKey{"half", 1}] = 5

	w.reconcileAppStatus(app)

	if got := appStatusOf(st, "half"); got != "degraded" {
		t.Fatalf("status = %q, want degraded (one replica still serving)", got)
	}
}
