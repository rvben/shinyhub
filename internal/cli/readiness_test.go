package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/appstatus"
)

// statusServer serves GET /api/apps/{slug} with a fixed observed status and
// an empty log tail, which is all the readiness helpers read.
func statusServer(t *testing.T, status string, redeployInFlight bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			return
		}
		fmt.Fprintf(w, `{"app":{"status":%q},"redeploy_in_flight":%v}`, status, redeployInFlight)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An elastic (grouped / per_session) app with no live worker reports idle:
// its first request boots a worker, so it is serving. The readiness gate must
// accept it; before it did, every `deploy --wait` and `fleet apply` on a
// grouped app waited the full timeout and then failed.
func TestPollAppStatus_IdleIsReady(t *testing.T) {
	srv := statusServer(t, "idle", false)
	ready, status, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
	if err != nil {
		t.Fatalf("pollAppStatus: %v", err)
	}
	if !ready || status != "idle" {
		t.Fatalf("ready=%v status=%q, want ready=true status=idle", ready, status)
	}
}

// A redeploy in flight still holds the gate whatever the observed status says,
// because the pool is about to be swapped.
func TestPollAppStatus_IdleWithRedeployInFlightIsNotReady(t *testing.T) {
	srv := statusServer(t, "idle", true)
	ready, _, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
	if err != nil {
		t.Fatalf("pollAppStatus: %v", err)
	}
	if ready {
		t.Fatal("ready = true with redeploy_in_flight, want false")
	}
}

// The gate is exactly the shared Serving predicate over every status the
// server can emit. This is what closes the class: a status added to
// appstatus.Observed is classified there once, and the gate follows.
func TestPollAppStatus_ReadyMatchesServingForEveryObservedStatus(t *testing.T) {
	for _, status := range appstatus.Observed {
		srv := statusServer(t, status, false)
		ready, got, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: pollAppStatus: %v", status, err)
		}
		if got != status {
			t.Errorf("%s: returned status %q", status, got)
		}
		if want := appstatus.Serving(status); ready != want {
			t.Errorf("%s: ready = %v, want %v (appstatus.Serving)", status, ready, want)
		}
	}
}

// The single-app timeout names the last status the server reported instead of
// asserting "still starting" for an app that may be stopped or degraded.
func TestWaitForHealthy_TimeoutNamesLastObservedStatus(t *testing.T) {
	prev := healthPollInterval
	healthPollInterval = time.Millisecond
	t.Cleanup(func() { healthPollInterval = prev })

	srv := statusServer(t, "degraded", false)
	var errOut bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 30*time.Millisecond, &errOut)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "still degraded") {
		t.Errorf("timeout error must name the last observed status, got: %v", err)
	}
	if strings.Contains(err.Error(), "still starting") {
		t.Errorf("timeout error must not claim starting for a degraded app, got: %v", err)
	}
}

// A status this CLI does not recognise is a version-skew signal: the server
// may consider the app healthy in a vocabulary this binary predates. The wait
// says so, on the first sighting and again in the timeout, rather than
// stalling silently.
func TestWaitForHealthy_UnknownStatusIsLoud(t *testing.T) {
	prev := healthPollInterval
	healthPollInterval = time.Millisecond
	t.Cleanup(func() { healthPollInterval = prev })

	srv := statusServer(t, "quantum", false)
	var errOut bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 30*time.Millisecond, &errOut)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), `"quantum"`) || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("timeout error must name the unrecognised status and suggest a CLI upgrade, got: %v", err)
	}
	if !strings.Contains(errOut.String(), "quantum") {
		t.Errorf("the unrecognised status must be reported while waiting, got stderr:\n%s", errOut.String())
	}
}

type dotSignalWriter struct {
	once sync.Once
	dot  chan struct{}
}

func (w *dotSignalWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(".")) {
		w.once.Do(func() { close(w.dot) })
	}
	return len(p), nil
}

func TestWaitForHealthy_CancellationInterruptsPollDelay(t *testing.T) {
	prev := healthPollInterval
	healthPollInterval = 10 * time.Second
	t.Cleanup(func() { healthPollInterval = prev })

	srv := statusServer(t, "starting", false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	progress := &dotSignalWriter{dot: make(chan struct{})}
	go func() {
		done <- waitForHealthyWithContext(ctx,
			&cliConfig{Host: srv.URL, Token: "tok"}, "demo", time.Minute, progress)
	}()

	// The dot is emitted immediately before the long retry delay. Waiting for it
	// makes this a deterministic regression for cancellation during that delay.
	select {
	case <-progress.dot:
	case <-time.After(time.Second):
		t.Fatal("readiness wait never entered its retry delay")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForHealthyWithContext error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitForHealthyWithContext did not stop promptly after cancellation")
	}
}

// The fleet loop's progress and timeout lines carry the observed status too.
func TestFleetHealthLoop_ProgressAndTimeoutNameLastStatus(t *testing.T) {
	var buf bytes.Buffer
	poll := func() (bool, string, error) { return false, "hibernated", nil }
	err := waitForFleetHealthLoop("demo", 60*time.Second, 2*time.Second, 10*time.Second,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "last status: hibernated") {
		t.Errorf("timeout error must carry the last status, got: %v", err)
	}
	if !strings.Contains(buf.String(), "still hibernated") {
		t.Errorf("progress lines must show the observed status, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "still starting") {
		t.Errorf("progress lines must not claim starting for a hibernated app, got:\n%s", buf.String())
	}
}

func TestFleetHealthLoop_UnknownStatusIsLoud(t *testing.T) {
	var buf bytes.Buffer
	poll := func() (bool, string, error) { return false, "quantum", nil }
	err := waitForFleetHealthLoop("demo", 60*time.Second, 2*time.Second, 10*time.Second,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), `"quantum"`) || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("timeout error must name the unrecognised status and suggest a CLI upgrade, got: %v", err)
	}
	if !strings.Contains(buf.String(), "quantum") {
		t.Errorf("the unrecognised status must be reported while waiting, got:\n%s", buf.String())
	}
}

// A progress line that never fires (short wait) must not hide the loud
// unknown-status notice: it is printed on first sighting, not on the cadence.
func TestFleetHealthLoop_UnknownStatusReportedBeforeFirstProgressLine(t *testing.T) {
	var buf bytes.Buffer
	poll := func() (bool, string, error) { return false, "quantum", nil }
	_ = waitForFleetHealthLoop("demo", 4*time.Second, 2*time.Second, time.Hour,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if !strings.Contains(buf.String(), "quantum") {
		t.Errorf("unrecognised status must be reported on first sighting, got:\n%s", buf.String())
	}
}

// `apps open` accepts idle: the app answers its URL by booting a worker.
func TestAppStatusOpenable_Idle(t *testing.T) {
	if !appStatusOpenable("idle") {
		t.Fatal("idle must be openable")
	}
	if !appStatusOpenable("IDLE") {
		t.Fatal("status matching is case-insensitive")
	}
}

// Every serving status is openable, and the launchpad contract's list (which
// the dashboard's launchpad-model.js mirrors) stays intact.
func TestAppStatusOpenable_ServingStatusesAreOpenable(t *testing.T) {
	for _, status := range appstatus.Observed {
		if appstatus.Serving(status) && !appStatusOpenable(status) {
			t.Errorf("appStatusOpenable(%q) = false for a serving status", status)
		}
	}
	for _, status := range []string{"healthy", "hibernated", "suspended", "deploying", "waking", "degraded"} {
		if !appStatusOpenable(status) {
			t.Errorf("appStatusOpenable(%q) = false, want true", status)
		}
	}
	for _, status := range []string{"stopped", "crashed", "deleting", ""} {
		if appStatusOpenable(status) {
			t.Errorf("appStatusOpenable(%q) = true, want false", status)
		}
	}
}
