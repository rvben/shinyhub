package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
)

// handoffDeferralBody is the server's verbatim refusal when a no-downtime
// deploy cannot use parallel generation handoff (internal/api/apps.go).
const handoffDeferralBody = `{"error":"working version preserved: parallel generation handoff currently supports multiplex isolation only; retry the CLI command with --allow-downtime (or send X-ShinyHub-Allow-Downtime: 1 via the API) to permit a stop-first deployment"}`

func deferralServer(t *testing.T, conflictHeader string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			if conflictHeader != "" {
				w.Header().Set("X-ShinyHub-Conflict", conflictHeader)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(handoffDeferralBody))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func deployAgainstDeferral(t *testing.T, srv *httptest.Server, preconditioned bool) (deployfail.Kind, error) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	spec := bundleBuildSpec{Dir: dir}
	digest, managedBy := "sha256:old", "fleet"
	var preconditions []*string
	if preconditioned {
		preconditions = []*string{&digest, &managedBy}
	}
	_, _, _, kind, err := deployAppBundleFromSpec(cfg, "demo", spec, "", "", io.Discard, "r", 5*time.Second, preconditions...)
	if err == nil {
		t.Fatal("expected the deploy to fail on the server's 409")
	}
	return kind, err
}

// A refused no-downtime handoff is a precondition the operator can clear with a
// flag, not a state race. Reporting it as "state changed under us; re-run plan"
// sends them to re-plan a fleet nothing changed under, and routes apply's
// recovery to the replan strategy, which cannot converge the app no matter how
// many times it runs.
func TestDeployAppBundle_HandoffDeferralIsNotAStalePlanConflict(t *testing.T) {
	kind, err := deployAgainstDeferral(t, deferralServer(t, "generation-handoff-deferred"), true)
	if isConflictError(err) {
		t.Error("a handoff deferral must not be classified as a precondition conflict")
	}
	if strings.Contains(err.Error(), "state changed under us") {
		t.Errorf("error must not claim remote state changed: %q", err)
	}
	if kind != deployfail.DowntimeRequired {
		t.Errorf("kind = %q, want downtime_required", kind)
	}
	if !strings.Contains(err.Error(), "--allow-downtime") {
		t.Errorf("error must name the remedy flag: %q", err)
	}
	if !strings.Contains(err.Error(), "multiplex isolation only") {
		t.Errorf("error must keep the server's reason: %q", err)
	}
	// The server's message already opens with "working version preserved".
	// The CLI prefix must add the slug and the fact of deferral, not restate it.
	if n := strings.Count(err.Error(), "working version"); n != 1 {
		t.Errorf("the CLI prefix duplicates the server's wording (%d occurrences): %q", n, err)
	}
}

// Negative control: this must not become a blanket declassification of 409s.
// A precondition conflict carrying no handoff header is a genuine state race
// and must keep the replan framing.
func TestDeployAppBundle_PlainPreconditionConflictStillReplans(t *testing.T) {
	kind, err := deployAgainstDeferral(t, deferralServer(t, ""), true)
	if !isConflictError(err) {
		t.Fatalf("a headerless precondition 409 must stay a conflict, got %q", err)
	}
	if !strings.Contains(err.Error(), "state changed under us") {
		t.Errorf("error = %q, want the replan framing", err)
	}
	if kind == deployfail.DowntimeRequired {
		t.Error("a state race must not be reported as downtime_required")
	}
}

// The deferral is a property of the deploy, not of the precondition headers.
// An unpreconditioned deploy (no --adopt reservation in play) gets the same 409
// and must classify identically instead of falling through to bundle_invalid.
func TestDeployAppBundle_HandoffDeferralClassifiedWithoutPreconditions(t *testing.T) {
	kind, err := deployAgainstDeferral(t, deferralServer(t, "generation-handoff-deferred"), false)
	if kind != deployfail.DowntimeRequired {
		t.Errorf("kind = %q, want downtime_required", kind)
	}
	if !strings.Contains(err.Error(), "--allow-downtime") {
		t.Errorf("error must name the remedy flag: %q", err)
	}
}

// A deferral is deterministic: the identical request will be refused again.
// Retrying burns the operator's health-timeout budget for no chance of success.
func TestDowntimeRequiredIsNotRetryable(t *testing.T) {
	if retryableDeployFailure(deployfail.DowntimeRequired) {
		t.Error("downtime_required must not be retried; the same request is refused identically")
	}
}
