package cli

import (
	"strings"
	"testing"
)

// TestAppsSleep sends a POST /api/apps/{slug}/sleep and reports the app is
// sleeping.
func TestAppsSleep(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"hibernated"}`)

	out, err := execCLI(t, "apps", "sleep", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" || req.Path != "/api/apps/demo/sleep" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}
	if !strings.Contains(out, "demo") {
		t.Errorf("output should name the app, got %q", out)
	}
}

// The JSON envelope is the agent-facing contract, so the status value is
// pinned rather than left to the prose line.
func TestAppsSleep_JSONReportsHibernated(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"hibernated"}`)

	out, err := execCLI(t, "apps", "sleep", "demo", "--output", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"status"`) || !strings.Contains(out, "hibernated") {
		t.Errorf("expected status=hibernated in JSON output, got %q", out)
	}
	if !strings.Contains(out, `"slug"`) {
		t.Errorf("expected slug in JSON output, got %q", out)
	}
}

// A 409 from the elastic or not-running guard must surface the server's reason,
// not a bare status code.
func TestAppsSleep_ServerErrorUnwrapped(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"sleep is not supported for grouped or per-session worker isolation"}`)

	_, err := execCLI(t, "apps", "sleep", "demo")
	if err == nil {
		t.Fatal("expected error for 409, got nil")
	}
	if !strings.Contains(err.Error(), "worker isolation") {
		t.Errorf("error should surface the server message, got %q", err.Error())
	}
}

func TestAppsSleep_NotFound(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"not found"}`)

	if _, err := execCLI(t, "apps", "sleep", "missing"); err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}
