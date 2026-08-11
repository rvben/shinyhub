package lifecycle

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// sleepTestWatcher wires the package fakes around a single app. deployFn is nil
// because SleepNow only tears down; it never boots.
func sleepTestWatcher(cfg Config, app *db.App) (*Watcher, *fakeStore, *fakeProxy, *fakeManager) {
	st := newFakeStore(
		map[string]*db.App{app.Slug: app},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	prx := newFakeProxy()
	mgr := &fakeManager{}
	return newTestWatcher(cfg, mgr, prx, st, nil), st, prx, mgr
}

func TestSleepNow_RunningAppBecomesHibernated(t *testing.T) {
	w, st, prx, mgr := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1})

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
	if len(prx.deregistered) != 1 || prx.deregistered[0] != "demo" {
		t.Errorf("deregistered = %v, want [demo]", prx.deregistered)
	}
	if len(mgr.stopped) != 1 || mgr.stopped[0] != "demo" {
		t.Errorf("stopped = %v, want [demo]", mgr.stopped)
	}
}

// A manual sleep is forceful: it drops in-flight sessions instead of aborting.
// hibernateNever makes BeginHibernate always refuse, which is how the fake
// models activeConns>0. Reaching "hibernated" anyway proves SleepNow does not
// route through the watcher's CAS, which would silently no-op on exactly the
// busy apps an operator most wants to act on.
func TestSleepNow_IsForcefulOnABusyApp(t *testing.T) {
	w, st, prx, _ := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1})
	prx.hibernateNever = true

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow on a busy app: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
}

// hibernate_timeout_minutes = 0 disables AUTOMATIC hibernation only (handleIdle
// returns early on it). An operator asking right now is a separate decision.
func TestSleepNow_AllowedWhenHibernationDisabled(t *testing.T) {
	zero := 0
	w, st, _, _ := sleepTestWatcher(Config{}, &db.App{
		ID: 1, Slug: "demo", Status: "running", Replicas: 1, HibernateTimeoutMinutes: &zero,
	})

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
}

// Sleep means release the resources. The warm floor is idle-path policy
// (handleIdle shrinks to it instead of hibernating) and an explicit operator
// action overrides policy.
func TestSleepNow_IgnoresWarmFloor(t *testing.T) {
	w, st, _, _ := sleepTestWatcher(Config{}, &db.App{
		ID: 1, Slug: "demo", Status: "running", Replicas: 3, MinWarmReplicas: 2,
	})

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
}

func TestSleepNow_RejectsNonRunningApp(t *testing.T) {
	for _, status := range []string{"stopped", "hibernated", "deploying", "crashed"} {
		t.Run(status, func(t *testing.T) {
			w, st, _, mgr := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: status, Replicas: 1})

			err := w.SleepNow("demo")
			if !errors.Is(err, ErrAppNotRunning) {
				t.Fatalf("err = %v, want ErrAppNotRunning", err)
			}
			if got := st.appStatus["demo"]; got != status {
				t.Errorf("status = %q, want %q (unchanged)", got, status)
			}
			if len(mgr.stopped) != 0 {
				t.Errorf("stopped = %v, want no teardown on a rejected sleep", mgr.stopped)
			}
		})
	}
}

// "degraded" means some replicas are still serving, and the card offers Sleep
// for it (UP_STATUSES in app-card-actions.js). Rejecting it here would make the
// advertised control 409 on exactly the apps whose replicas are worth
// reclaiming, so it tears down like "running".
func TestSleepNow_DegradedAppSleeps(t *testing.T) {
	w, st, prx, mgr := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "degraded", Replicas: 2})

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow on a degraded app: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
	if len(prx.deregistered) != 1 || prx.deregistered[0] != "demo" {
		t.Errorf("deregistered = %v, want [demo]", prx.deregistered)
	}
	if len(mgr.stopped) != 1 || mgr.stopped[0] != "demo" {
		t.Errorf("stopped = %v, want [demo]", mgr.stopped)
	}
}

// Elastic pools keep their live backends in a workers map with no replica rows,
// so hibernatePool's `for i := 0; i < app.Replicas; i++` loop is the wrong model
// for them.
func TestSleepNow_RejectsElasticApp(t *testing.T) {
	for _, iso := range []string{"grouped", "per_session"} {
		t.Run(iso, func(t *testing.T) {
			w, st, _, mgr := sleepTestWatcher(Config{}, &db.App{
				ID: 1, Slug: "demo", Status: "running", Replicas: 1, WorkerIsolation: iso,
			})

			err := w.SleepNow("demo")
			if !errors.Is(err, ErrElasticNotSleepable) {
				t.Fatalf("err = %v, want ErrElasticNotSleepable", err)
			}
			if got := st.appStatus["demo"]; got != "running" {
				t.Errorf("status = %q, want running (unchanged)", got)
			}
			if len(mgr.stopped) != 0 {
				t.Errorf("stopped = %v, want no teardown on a rejected sleep", mgr.stopped)
			}
		})
	}
}

// The isolation mode is resolved against the fleet default, so an app that
// leaves worker_isolation empty on an elastic-by-default server is still
// rejected. Reading app.WorkerIsolation raw would miss this.
func TestSleepNow_RejectsElasticViaFleetDefault(t *testing.T) {
	w, _, _, _ := sleepTestWatcher(
		Config{DefaultWorkerIsolation: "per_session"},
		&db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1},
	)

	if err := w.SleepNow("demo"); !errors.Is(err, ErrElasticNotSleepable) {
		t.Fatalf("err = %v, want ErrElasticNotSleepable", err)
	}
}

// The negative control for the fleet default: an empty per-app mode on a
// default server resolves to multiplex and must still sleep.
func TestSleepNow_MultiplexByDefaultStillSleeps(t *testing.T) {
	w, st, _, _ := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1})

	if err := w.SleepNow("demo"); err != nil {
		t.Fatalf("SleepNow: %v", err)
	}
	if got := st.appStatus["demo"]; got != "hibernated" {
		t.Errorf("status = %q, want hibernated", got)
	}
}

// When the manager's Stop fails the pool never reached a terminal state.
// Persisting "hibernated" would assert a state that is not real and would strand
// a live manager entry that trips ErrReplicaAlreadyRunning on the next wake.
func TestSleepNow_TeardownFailureLeavesStatusUnchanged(t *testing.T) {
	w, st, _, mgr := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1})
	mgr.stopErr = errors.New("process refused to die")

	err := w.SleepNow("demo")
	if !errors.Is(err, ErrSleepTeardownFailed) {
		t.Fatalf("err = %v, want ErrSleepTeardownFailed", err)
	}
	if got := st.appStatus["demo"]; got != "running" {
		t.Errorf("status = %q, want running (unchanged)", got)
	}
}

func TestSleepNow_UnknownAppErrors(t *testing.T) {
	w, _, _, _ := sleepTestWatcher(Config{}, &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1})

	if err := w.SleepNow("nope"); err == nil {
		t.Fatal("expected an error for an unknown slug")
	}
}
