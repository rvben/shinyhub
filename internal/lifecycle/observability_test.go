package lifecycle

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// captureWarnings swaps the default slog logger for one writing JSON at warn+
// to a buffer, returning the buffer and a restore func.
func captureWarnings(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf, func() { slog.SetDefault(prev) }
}

// runningMgr reports one running replica for "app" so runOnce drives the idle
// check. stopErr, when non-nil, is returned by Stop after recording the call.
func runningMgr(stopErr error) *fakeManager {
	return &fakeManager{
		entries: []*process.ProcessInfo{{Slug: "app", Index: 0, Status: process.StatusRunning}},
		stopErr: stopErr,
	}
}

// idleHibernationWatcher builds a watcher and store for an app that is idle past
// its timeout, ready for handleIdle to attempt hibernation.
func idleHibernationWatcher(mgr *fakeManager, st *fakeStore) *Watcher {
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	return newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
}

// idleApp is a running app with one running replica row, so reconcileStatuses
// leaves it "running" and the hibernation path under test is what acts.
func idleApp() *fakeStore {
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID: 1, Slug: "app", Status: "running", Replicas: 1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "running"}}}
	return st
}

// TestHibernation_LogsReplicaPersistFailure proves that when the best-effort
// UpsertReplica write fails during hibernation, the watcher logs a warning
// instead of silently swallowing the error - so a broken DB is visible.
func TestHibernation_LogsReplicaPersistFailure(t *testing.T) {
	st := idleApp()
	st.upsertErr = errors.New("disk full")
	w := idleHibernationWatcher(runningMgr(nil), st)

	buf, restore := captureWarnings(t)
	defer restore()
	w.runOnce()

	logs := buf.String()
	if !strings.Contains(logs, "disk full") || !strings.Contains(logs, "\"slug\":\"app\"") {
		t.Fatalf("expected a warning naming the slug and error, got:\n%s", logs)
	}
}

// TestHibernation_LogsStatusUpdateFailure proves the app-status write failure
// during hibernation is logged rather than dropped.
func TestHibernation_LogsStatusUpdateFailure(t *testing.T) {
	st := idleApp()
	st.updateStatusErr = errors.New("db locked")
	w := idleHibernationWatcher(runningMgr(nil), st)

	buf, restore := captureWarnings(t)
	defer restore()
	w.runOnce()

	if !strings.Contains(buf.String(), "db locked") {
		t.Fatalf("expected a warning carrying the status-update error, got:\n%s", buf.String())
	}
}

// TestHibernation_LogsStopFailure proves a manager.Stop failure during
// hibernation is logged rather than dropped.
func TestHibernation_LogsStopFailure(t *testing.T) {
	w := idleHibernationWatcher(runningMgr(errors.New("kill refused")), idleApp())

	buf, restore := captureWarnings(t)
	defer restore()
	w.runOnce()

	if !strings.Contains(buf.String(), "kill refused") {
		t.Fatalf("expected a warning carrying the stop error, got:\n%s", buf.String())
	}
}

// TestRecovery_LogsCrashMarkFailure proves that when re-adoption cannot persist
// a replica's "crashed" status (here: the store is closed), recovery logs a
// warning instead of swallowing it - a dropped crash-mark would otherwise leave
// the replica permanently un-restarted.
func TestRecovery_LogsCrashMarkFailure(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store.Close() // force the UpsertReplica write to fail

	buf, restore := captureWarnings(t)
	defer restore()

	app := &db.App{ID: 1, Slug: "myapp"}
	r := &db.Replica{Index: 0} // PID nil -> crash-mark branch; mgr/prx untouched
	recoverNativeReplica(store, nil, nil, app, r, "/bundles/v1")

	if !strings.Contains(buf.String(), "myapp") {
		t.Fatalf("expected a warning when the crash-mark persist fails, got:\n%s", buf.String())
	}
}

// TestReconcile_LogsStatusUpdateFailure proves the status-reconciliation write
// failure is logged rather than dropped.
func TestReconcile_LogsStatusUpdateFailure(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		nil,
	)
	// One desired replica but it is crashed, so reconcile wants "degraded".
	pid := 0
	_ = pid
	st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "crashed"}}}
	st.updateStatusErr = errors.New("db locked")
	mgr := &fakeManager{entries: []*process.ProcessInfo{{Slug: "app", Index: 0, Status: process.StatusRunning}}}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	buf, restore := captureWarnings(t)
	defer restore()
	w.reconcileAppStatus(st.apps["app"], st.replicas[1])

	if !strings.Contains(buf.String(), "db locked") {
		t.Fatalf("expected a warning carrying the reconcile status error, got:\n%s", buf.String())
	}
}
