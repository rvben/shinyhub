package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resetAppsSetFlags restores defaults so tests don't leak state between runs.
// appsSetCmd is a package-global, so without this tests inherit the flag
// values and Changed markers from whatever ran before them.
func resetAppsSetFlags(t *testing.T) {
	t.Helper()
	appsSetFlags.hibernateTimeout = 0
	appsSetFlags.replicas = 0
	appsSetFlags.maxSessionsPerReplica = -1
	for _, name := range []string{"hibernate-timeout", "replicas", "max-sessions-per-replica"} {
		f := appsSetCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag %q not defined on appsSetCmd", name)
		}
		f.Changed = false
	}
}

func TestAppsSet_ReplicasOnly(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--replicas", "3"})
	if err := appsCmd.Execute(); err != nil {
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
	if got := body["replicas"]; got != float64(3) {
		t.Errorf("expected replicas=3, got %v (%T)", got, got)
	}
	if _, present := body["max_sessions_per_replica"]; present {
		t.Errorf("expected max_sessions_per_replica to be absent, got %v", body["max_sessions_per_replica"])
	}
	if _, present := body["hibernate_timeout_minutes"]; present {
		t.Errorf("expected hibernate_timeout_minutes to be absent, got %v", body["hibernate_timeout_minutes"])
	}
}

func TestAppsSet_MaxSessionsOnly(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--max-sessions-per-replica", "25"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got := body["max_sessions_per_replica"]; got != float64(25) {
		t.Errorf("expected max_sessions_per_replica=25, got %v", got)
	}
	if _, present := body["replicas"]; present {
		t.Errorf("expected replicas to be absent")
	}
}

// Passing 0 explicitly means "reset to runtime default" and must still hit
// the wire as 0 (not a missing key). Matches the server's semantics.
func TestAppsSet_MaxSessionsZeroResetsToDefault(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--max-sessions-per-replica", "0"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	v, present := body["max_sessions_per_replica"]
	if !present {
		t.Fatalf("expected max_sessions_per_replica to be present (value 0), got absent")
	}
	if v != float64(0) {
		t.Errorf("expected 0, got %v", v)
	}
}

func TestAppsSet_CombinedFlags(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo",
		"--replicas", "2",
		"--max-sessions-per-replica", "15",
		"--hibernate-timeout", "45",
	})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["replicas"] != float64(2) {
		t.Errorf("replicas: got %v", body["replicas"])
	}
	if body["max_sessions_per_replica"] != float64(15) {
		t.Errorf("max_sessions_per_replica: got %v", body["max_sessions_per_replica"])
	}
	if body["hibernate_timeout_minutes"] != float64(45) {
		t.Errorf("hibernate_timeout_minutes: got %v", body["hibernate_timeout_minutes"])
	}
}

func TestAppsSet_RejectsReplicasZero(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--replicas", "0"})
	err := appsCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), ">= 1") {
		t.Errorf("expected '--replicas must be >= 1', got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

func TestAppsSet_RejectsMaxSessionsOutOfRange(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--max-sessions-per-replica", "1001"})
	err := appsCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1000") {
		t.Errorf("expected 'between 0 and 1000' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

func TestAppsSet_RequiresAtLeastOneFlag(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo"})
	err := appsCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least one flag") {
		t.Errorf("expected 'at least one flag' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests, got %d", len(*reqs))
	}
}

// TestAppsLogs_ServerErrorExitsNonZero asserts that a 4xx/5xx from the log
// streaming endpoint is returned as a non-nil error (exit non-zero in the CLI).
func TestAppsLogs_ServerErrorExitsNonZero(t *testing.T) {
	// runAppsLogs uses http.DefaultClient (no timeout) for SSE; point it at a
	// real httptest server accessible over the loopback interface.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"app not found"}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	appsCmd.SetArgs([]string{"logs", "noapp"})
	err := appsCmd.Execute()
	if err == nil {
		t.Fatal("expected non-nil error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should contain status code 404, got: %v", err)
	}
}

