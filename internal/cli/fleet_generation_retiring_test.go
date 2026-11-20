package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

// retiringBody is the server's verbatim refusal when a deploy has to wait for
// an earlier version of the app (internal/api/apps.go).
const retiringBody = `{"error":"working versions preserved: a stop-first deploy must wait for all previous generations to retire"}`

func deployAgainstRetiringVersion(t *testing.T, remedy string) (deployfail.Kind, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			w.Header().Set("X-ShinyHub-Conflict", "generation-handoff-deferred")
			if remedy != "" {
				w.Header().Set("X-ShinyHub-Conflict-Remedy", remedy)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(retiringBody))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	_, _, _, kind, err := deployAppBundleFromSpec(&cliConfig{Host: srv.URL, Token: "shk_test"}, "demo", bundleBuildSpec{Dir: dir}, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected the deploy to fail on the server's 409")
	}
	return kind, err
}

// A deploy refused because an earlier version is still retiring is cleared by
// time alone. It must not be reported under the kind whose remedy is consent
// to downtime, which changes nothing here.
func TestDeployAppBundle_RetiringVersionIsItsOwnKind(t *testing.T) {
	kind, err := deployAgainstRetiringVersion(t, "wait")
	if kind != deployfail.GenerationRetiring {
		t.Errorf("kind = %q, want generation_retiring", kind)
	}
	if isConflictError(err) {
		t.Error("a wait for a retiring version must not be classified as a precondition conflict")
	}
	if !strings.Contains(err.Error(), "previous generations to retire") {
		t.Errorf("error must keep the server's reason: %q", err)
	}
}

// A server that does not name the remedy keeps the kind it always had, and an
// unknown remedy is not read as a wait.
func TestDeployAppBundle_DeferralWithoutAWaitRemedyStaysDowntimeRequired(t *testing.T) {
	for _, remedy := range []string{"", "something-else"} {
		if kind, _ := deployAgainstRetiringVersion(t, remedy); kind != deployfail.DowntimeRequired {
			t.Errorf("remedy %q: kind = %q, want downtime_required", remedy, kind)
		}
	}
}

// retiringResult is an apply result for a deploy the server declined before it
// changed anything, which is the state the apply loop records for it.
func retiringResult(slug string) applyResult {
	res := deployFailedResult(slug, deployfail.GenerationRetiring)
	res.mutation = mutationNone
	return res
}

// Re-running apply is the whole remedy for a retiring version, so the recovery
// must say so in the fields an automated caller reads.
func TestApplyRecovery_RetiringVersionIsSafeToRetry(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{retiringResult("analytics")})
	if got.Strategy != "resume" || !got.SafeToRetry {
		t.Errorf("strategy = %q safe_to_retry = %v, want resume and true", got.Strategy, got.SafeToRetry)
	}
	if !strings.Contains(got.Summary, "analytics") || !strings.Contains(got.Summary, "still retiring") {
		t.Errorf("summary must name the app and the wait: %q", got.Summary)
	}
	if strings.Contains(got.Summary, "Fix the reported") {
		t.Errorf("nothing is broken to fix: %q", got.Summary)
	}
	if !strings.Contains(joinCommands(got), testResumeCommand) {
		t.Errorf("recovery must carry the resume command: %q", joinCommands(got))
	}
}

// One app that needs a repair keeps the whole run out of the safe-to-retry
// class even when another only has to wait.
func TestApplyRecovery_RetiringVersionDoesNotVouchForAnotherFailure(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		retiringResult("analytics"),
		deployFailedResult("charts", deployfail.BuildFailed),
	})
	if got.Strategy != "repair_then_resume" || got.SafeToRetry {
		t.Errorf("strategy = %q safe_to_retry = %v, want repair_then_resume and false", got.Strategy, got.SafeToRetry)
	}
}

// The refusal itself changes nothing, but the run may have changed the app
// before it and then failed to put that back. Such a result is not a plain
// wait: retrying blind is not safe and "nothing was changed" would be false.
func TestApplyRecovery_RetiringVersionAfterAnUnknownMutationIsNotAPlainWait(t *testing.T) {
	res := deployFailedResult("analytics", deployfail.GenerationRetiring)
	res.mutation = mutationUnknown
	got := applyRecoveryFor(recoveryCtx(), []applyResult{res})
	if got.Strategy != "repair_then_resume" || got.SafeToRetry {
		t.Errorf("strategy = %q safe_to_retry = %v, want repair_then_resume and false", got.Strategy, got.SafeToRetry)
	}
	if strings.Contains(got.Summary, "Nothing was changed") {
		t.Errorf("summary claims nothing changed for an unknown mutation state: %q", got.Summary)
	}
}

// adoptAgainstRetiringVersion runs an ordinary adoption whose upload the server
// declines because an earlier version is still retiring. releaseStatus is the
// server's answer to the PATCH that puts the ownership reservation back.
func adoptAgainstRetiringVersion(t *testing.T, releaseStatus int) (applyResult, []any) {
	t.Helper()
	var mu sync.Mutex
	var managedBy []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.Header().Set("X-ShinyHub-Conflict", "generation-handoff-deferred")
			w.Header().Set("X-ShinyHub-Conflict-Remedy", "wait")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(retiringBody))
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			value, stamped := body["managed_by"]
			if stamped {
				mu.Lock()
				managedBy = append(managedBy, value)
				mu.Unlock()
			}
			if stamped && value == nil {
				w.WriteHeader(releaseStatus)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	res := convergeApp(&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt},
		fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"},
		fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	mu.Lock()
	defer mu.Unlock()
	return res, append([]any(nil), managedBy...)
}

// An adoption reserves ownership before it uploads. When the upload is declined
// for a retiring version and the reservation is put back, the app is as it was
// found, so the run is a plain wait like any other.
func TestConvergeApp_AdoptDeclinedForARetiringVersionIsAPlainWait(t *testing.T) {
	res, managedBy := adoptAgainstRetiringVersion(t, http.StatusOK)
	if res.status != statusFailed || resultFailureKind(res) != string(deployfail.GenerationRetiring) {
		t.Fatalf("status = %s kind = %q (%v), want failed generation_retiring", res.status, resultFailureKind(res), res.err)
	}
	if len(managedBy) != 2 || managedBy[0] != "fleet:eu" || managedBy[1] != nil {
		t.Fatalf("managed_by sequence = %v, want the reservation and its release", managedBy)
	}
	if got := resultMutationState(res); got != mutationNone {
		t.Errorf("mutation = %q, want none once the reservation is released", got)
	}
	if got := applyRecoveryFor(recoveryCtx(), []applyResult{res}); got.Strategy != "resume" || !got.SafeToRetry {
		t.Errorf("strategy = %q safe_to_retry = %v, want resume and true", got.Strategy, got.SafeToRetry)
	}
}

// If the reservation cannot be put back, the app is left owned by a fleet that
// never deployed to it. That is a change, and it must not be reported as a wait.
func TestConvergeApp_AdoptDeclinedForARetiringVersionWithAStuckReservationIsNotAPlainWait(t *testing.T) {
	res, _ := adoptAgainstRetiringVersion(t, http.StatusInternalServerError)
	if got := resultMutationState(res); got != mutationUnknown {
		t.Errorf("mutation = %q, want unknown while the reservation is not released", got)
	}
	if got := applyRecoveryFor(recoveryCtx(), []applyResult{res}); got.Strategy != "repair_then_resume" || got.SafeToRetry {
		t.Errorf("strategy = %q safe_to_retry = %v, want repair_then_resume and false", got.Strategy, got.SafeToRetry)
	}
}

// A retried upload proves only that the last attempt did not promote. When an
// earlier attempt lost its response, the bundle may be live, so the adoption
// must not be reported as having changed nothing.
func TestConvergeApp_AdoptDeclinedAfterALostResponseIsNotAPlainWait(t *testing.T) {
	res := declinedAfterALostResponse(t, fleet.ActionAdopt, true)
	if got := resultMutationState(res); got == mutationNone {
		t.Errorf("mutation = %q after an upload whose outcome is unknown, want it not reported as none", got)
	}
	if got := applyRecoveryFor(recoveryCtx(), []applyResult{res}); got.SafeToRetry {
		t.Errorf("safe_to_retry = true after an upload whose outcome is unknown")
	}
}

// The same holds for an ordinary source update, which has no reservation to
// read back: the retry's refusal does not vouch for the upload before it.
func TestConvergeApp_UpdateDeclinedAfterALostResponseIsNotSafeToRetry(t *testing.T) {
	res := declinedAfterALostResponse(t, fleet.ActionUpdateSource, false)
	if resultFailureKind(res) != string(deployfail.GenerationRetiring) {
		t.Fatalf("kind = %q (%v), want generation_retiring", resultFailureKind(res), res.err)
	}
	if got := applyRecoveryFor(recoveryCtx(), []applyResult{res}); got.SafeToRetry {
		t.Errorf("safe_to_retry = true after an upload whose outcome is unknown")
	}
}

// declinedAfterALostResponse converges one app against a server that drops the
// first upload without answering and declines the retry for a retiring version.
func declinedAfterALostResponse(t *testing.T, action fleet.Action, adopt bool) applyResult {
	t.Helper()
	var mu sync.Mutex
	uploads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			mu.Lock()
			uploads++
			first := uploads == 1
			mu.Unlock()
			_, _ = io.Copy(io.Discard, r.Body)
			if first {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			w.Header().Set("X-ShinyHub-Conflict", "generation-handoff-deferred")
			w.Header().Set("X-ShinyHub-Conflict-Remedy", "wait")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(retiringBody))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	res := convergeApp(&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "legacy", Action: action},
		fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"},
		fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: adopt, preconditions: true, retries: 1, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	mu.Lock()
	defer mu.Unlock()
	if uploads != 2 {
		t.Fatalf("uploads = %d, want the lost one and its retry", uploads)
	}
	return res
}
