package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/safego"
)

// A lifetime backstop re-armed by a live settings change runs runtime code
// (the app operation lock, proxy and terminate) on a timer goroutine, exactly
// like the original arm. A panic there must be contained, not crash the
// server.
func TestUpdateSessionLifetime_ReArmedBackstopContainsPanic(t *testing.T) {
	fired := make(chan struct{})
	s := &ElasticSpawner{
		AcquireAppOperation: func(string) (func(), error) {
			close(fired)
			panic("operation lock fault")
		},
	}
	old := &elasticLifetime{
		timer:   time.NewTimer(time.Hour),
		started: time.Now().Add(-time.Hour),
		limit:   time.Second,
		slot:    0,
	}
	s.lifetimeTimers.Store("app/0", old)

	// The extended deadline is still in the past, so the replacement fires now.
	s.UpdateSessionLifetime("app", 10)

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("re-armed lifetime backstop never fired")
	}
	// Give an uncontained panic time to take the test binary down.
	time.Sleep(100 * time.Millisecond)
}

// runFatalChild re-executes the named test in a child process with
// FATAL_PANIC_TEST_CHILD set, and returns the child's combined output and error.
// A re-panicked safego.Fatal on a background goroutine ends the process, which
// no recover in the test itself can observe.
func runFatalChild(t *testing.T, name string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "FATAL_PANIC_TEST_CHILD=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func newWakePanicWatcher(t *testing.T, panicValue any) (*Watcher, *fakeStore) {
	t.Helper()
	st := newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "waking", Replicas: 1},
	}, nil)
	st.deployments = []*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)
	w.SetConsumerBootGate(func(int64) (func(), error) { panic(panicValue) })
	return w, st
}

// An ordinary panic in a wake is contained: the app is reverted out of the
// transient waking state and the server keeps running.
func TestDriveWakingApp_OrdinaryPanicIsContained(t *testing.T) {
	w, st := newWakePanicWatcher(t, "wake fault")
	done := w.driveWakingApp(context.Background(), "app", "reconcile")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wake did not finish")
	}
	waitNotWaking(t, st, "app")
}

// A safego.Fatal raised inside a wake must not be absorbed by the wake's own
// recover: the process has to exit.
func TestDriveWakingApp_FatalPanicPropagates(t *testing.T) {
	if os.Getenv("FATAL_PANIC_TEST_CHILD") == "1" {
		w, _ := newWakePanicWatcher(t, safego.Fatal{Value: "wake-fatal-marker"})
		done := w.driveWakingApp(context.Background(), "app", "reconcile")
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return // reaching here means the Fatal was absorbed
	}
	out, err := runFatalChild(t, "TestDriveWakingApp_FatalPanicPropagates")
	if err == nil {
		t.Fatalf("child process survived a safego.Fatal raised in a wake; output:\n%s", out)
	}
	if !strings.Contains(out, "panic: (safego.Fatal)") {
		t.Fatalf("child exited (%v) without dying of the safego.Fatal:\n%s", err, out)
	}
}
