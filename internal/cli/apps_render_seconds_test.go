package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAppsSet_RenderSecondsOnly asserts that --render-seconds is the only
// flag required (it must not trip the "at least one flag is required" gate)
// and that it sends render_seconds in the PATCH body as a float.
func TestAppsSet_RenderSecondsOnly(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "1.3"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "PATCH" || req.Path != "/api/apps/demo" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got := body["render_seconds"]; got != float64(1.3) {
		t.Errorf("expected render_seconds=1.3, got %v (%T)", got, got)
	}
	if _, present := body["replicas"]; present {
		t.Errorf("expected replicas to be absent, got %v", body["replicas"])
	}
}

// TestAppsSet_RenderSecondsAdvisoryPrinted asserts that when the server
// response's render_pacing block suggests a cap below the current effective
// cap, the CLI prints a one-line advisory pointing at
// --max-sessions-per-replica.
func TestAppsSet_RenderSecondsAdvisoryPrinted(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{
		"app": {"slug": "demo"},
		"render_pacing": {
			"render_seconds": 1.3,
			"effective_cores": 2,
			"cores_source": "cgroup",
			"suggested_max_sessions_per_replica": 4,
			"current_effective_max_sessions_per_replica": 25,
			"cadence_assumption_seconds": 5
		}
	}`)

	out, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "1.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if !strings.Contains(out, "Render pacing on (render_seconds=1.3)") {
		t.Errorf("expected render pacing advisory in output, got %q", out)
	}
	if !strings.Contains(out, "consider --max-sessions-per-replica 4 (current effective: 25)") {
		t.Errorf("expected advisory to name the suggested and current caps, got %q", out)
	}
}

// TestAppsSet_RenderSecondsAdvisoryOnStderrKeepsJSONStdoutClean asserts that
// the cap-sizing advisory goes to stderr, not stdout: in -o json mode stdout
// must be exactly the machine-readable envelope (a single JSON document with
// no trailing text) while the advisory line lands on stderr. This is the
// regression check for the advisory being printed with cmd.OutOrStdout(),
// which corrupted `apps set ... -o json` output.
func TestAppsSet_RenderSecondsAdvisoryOnStderrKeepsJSONStdoutClean(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{
		"app": {"slug": "demo"},
		"render_pacing": {
			"render_seconds": 1.3,
			"effective_cores": 2,
			"cores_source": "cgroup",
			"suggested_max_sessions_per_replica": 4,
			"current_effective_max_sessions_per_replica": 25,
			"cadence_assumption_seconds": 5
		}
	}`)

	stdout, stderr, err := execCLISplit(t, "apps", "set", "demo", "--render-seconds", "1.3", "-o", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}

	if !strings.Contains(stderr, "Render pacing on (render_seconds=1.3)") {
		t.Errorf("expected render pacing advisory on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "consider --max-sessions-per-replica 4 (current effective: 25)") {
		t.Errorf("expected advisory to name the suggested and current caps on stderr, got %q", stderr)
	}
	if strings.Contains(stdout, "Render pacing on") {
		t.Errorf("advisory must not appear on stdout, got %q", stdout)
	}

	var obj map[string]any
	trimmed := strings.TrimSpace(stdout)
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		t.Fatalf("stdout is not a single JSON document: %q: %v", stdout, err)
	}
	if obj["status"] != "updated" {
		t.Errorf("stdout status = %v, want %q", obj["status"], "updated")
	}
	if obj["slug"] != "demo" {
		t.Errorf("stdout slug = %v, want %q", obj["slug"], "demo")
	}
}

// TestAppsSet_RenderSecondsNoAdvisoryWhenCapNotLower is the negative control
// for TestAppsSet_RenderSecondsAdvisoryPrinted: when the suggested cap is not
// below the current effective cap, no advisory line is printed.
func TestAppsSet_RenderSecondsNoAdvisoryWhenCapNotLower(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{
		"app": {"slug": "demo"},
		"render_pacing": {
			"render_seconds": 1.3,
			"effective_cores": 2,
			"cores_source": "cgroup",
			"suggested_max_sessions_per_replica": 25,
			"current_effective_max_sessions_per_replica": 25,
			"cadence_assumption_seconds": 5
		}
	}`)

	out, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "1.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if strings.Contains(out, "Render pacing on") {
		t.Errorf("expected no advisory when suggested cap is not below current, got %q", out)
	}
}

// TestAppsSet_RenderSecondsNoAdvisoryWhenPacingAbsent covers a response with
// no render_pacing block at all (e.g. render_seconds not sent, or the server
// omits it because render_seconds <= 0): no advisory line is printed.
func TestAppsSet_RenderSecondsNoAdvisoryWhenPacingAbsent(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"app": {"slug": "demo"}}`)

	out, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if strings.Contains(out, "Render pacing on") {
		t.Errorf("expected no advisory when render_pacing is absent, got %q", out)
	}
}

// TestAppsSet_RejectsRenderSecondsNegative mirrors
// TestAppsSet_RejectsMaxSessionsNegativeOne: a client-side range check
// rejects an out-of-range value before any HTTP request is sent.
func TestAppsSet_RejectsRenderSecondsNegative(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "-1")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 600") {
		t.Errorf("expected 'between 0 and 600' error for -1, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// TestAppsSet_RejectsRenderSecondsOutOfRange asserts the upper bound is also
// enforced client-side.
func TestAppsSet_RejectsRenderSecondsOutOfRange(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--render-seconds", "600.1")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 600") {
		t.Errorf("expected 'between 0 and 600' error for 600.1, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}
