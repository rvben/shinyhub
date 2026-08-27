package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// --verify-health asserts that every app is in a state it serves from without
// operator action. A parked app (hibernated after its idle timeout, or
// suspended by the runtime) wakes on its next request, so it is settled: the
// gate must accept it in one poll rather than waiting out --health-timeout for
// a wake that nothing in the gate ever triggers.
func TestVerifyFleetHealthy_ParkedIsSettled(t *testing.T) {
	for _, status := range []string{"hibernated", "suspended"} {
		t.Run(status, func(t *testing.T) {
			srv := statusServer(t, status, false)
			var out bytes.Buffer
			start := time.Now()
			err := verifyFleetHealthy(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", &out, 5*time.Second)
			if err != nil {
				t.Fatalf("a %s app must satisfy --verify-health, got: %v", status, err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("a %s app must be accepted on the first poll, took %s", status, elapsed)
			}
			got := out.String()
			if !strings.Contains(got, status) || !strings.Contains(got, "wakes on") {
				t.Fatalf("the gate must say the app is parked and wakes on demand, got:\n%s", got)
			}
		})
	}
}

// The post-deploy wait is stricter on purpose: a deploy restarts the app, so
// the server reports running (or idle for an elastic pool) once it is up. A
// parked status there is not the deploy's outcome, and the wait keeps
// requiring a serving replica. This pins that widening the verify gate did not
// widen the readiness predicate itself.
func TestWaitForFleetHealthy_ParkedStillWaits(t *testing.T) {
	srv := statusServer(t, "hibernated", false)
	var out bytes.Buffer
	err := waitForFleetHealthy(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", &out, 50*time.Millisecond)
	if err == nil {
		t.Fatal("the post-deploy wait must not accept hibernated as deployed-and-serving")
	}
	if !strings.Contains(err.Error(), "last status: hibernated") {
		t.Fatalf("timeout must name the last status, got: %v", err)
	}
}

// The loop's own deadline can expire while the final poll is in flight. That
// cancellation is the wait budget running out, not evidence the server stopped
// answering, so the timeout names the last observed status and nothing else.
func TestFleetHealthLoop_DeadlineExpiryDuringPollIsNotAServerError(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	// stepClock(2s) with a 6s budget: polls run at t=0, 2s and 4s; the third
	// poll's completion is measured at t=6s, on the deadline, and that is the
	// one that fails with a cancellation-shaped error.
	poll := func() (bool, string, error) {
		calls++
		if calls == 3 {
			return false, "", errors.New("the ShinyHub server at http://example did not answer within 30s")
		}
		return false, "starting", nil
	}
	err := waitForFleetHealthLoop("demo", 6*time.Second, 2*time.Second, time.Hour,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "last status: starting") {
		t.Errorf("timeout must carry the last observed status, got: %v", err)
	}
	if strings.Contains(err.Error(), "last error") || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("a poll cancelled by the wait deadline must not be reported as a server failure, got: %v", err)
	}
}

// A transient poll failure that later polls recover from is not the reason the
// wait timed out. Only a failure on the most recent poll is worth naming.
func TestFleetHealthLoop_RecoveredPollErrorIsNotReported(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	poll := func() (bool, string, error) {
		calls++
		if calls == 2 {
			return false, "", errors.New("the ShinyHub server at http://example did not answer within 30s")
		}
		return false, "starting", nil
	}
	err := waitForFleetHealthLoop("demo", 10*time.Second, 2*time.Second, time.Hour,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "last status: starting") {
		t.Errorf("timeout must carry the last observed status, got: %v", err)
	}
	if strings.Contains(err.Error(), "last error") {
		t.Errorf("an error the next poll recovered from must not be named as the cause, got: %v", err)
	}
}

// When the most recent poll genuinely failed before the deadline, that failure
// is still the actionable diagnostic and stays in the message.
func TestFleetHealthLoop_PersistentPollErrorIsReported(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	poll := func() (bool, string, error) {
		calls++
		if calls == 1 {
			return false, "starting", nil
		}
		return false, "", errors.New("HTTP 502 from the proxy")
	}
	err := waitForFleetHealthLoop("demo", 10*time.Second, 2*time.Second, time.Hour,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "last error: HTTP 502") {
		t.Errorf("a failure that persisted to the end must be named, got: %v", err)
	}
}
