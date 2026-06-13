package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppsSet_ReplicasOnly(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--replicas", "3", "--yes"); err != nil {
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

	if _, err := execCLI(t, "apps", "set", "demo", "--max-sessions-per-replica", "25"); err != nil {
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

	if _, err := execCLI(t, "apps", "set", "demo", "--max-sessions-per-replica", "0"); err != nil {
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

	if _, err := execCLI(t, "apps", "set", "demo",
		"--replicas", "2",
		"--max-sessions-per-replica", "15",
		"--hibernate-timeout", "45",
		"--yes",
	); err != nil {
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

func TestAppsSet_AutoscaleEnable(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo",
		"--autoscale",
		"--autoscale-min", "1",
		"--autoscale-max", "4",
		"--autoscale-target", "0.7",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	as, ok := body["autoscale"].(map[string]any)
	if !ok {
		t.Fatalf("expected an autoscale object, got %v (%T)", body["autoscale"], body["autoscale"])
	}
	if as["enabled"] != true {
		t.Errorf("enabled: got %v", as["enabled"])
	}
	if as["min_replicas"] != float64(1) {
		t.Errorf("min_replicas: got %v", as["min_replicas"])
	}
	if as["max_replicas"] != float64(4) {
		t.Errorf("max_replicas: got %v", as["max_replicas"])
	}
	if as["target"] != 0.7 {
		t.Errorf("target: got %v", as["target"])
	}
}

// Disabling sends only the enabled flag; the other fields stay untouched so the
// server keeps the stored bounds for a later re-enable.
func TestAppsSet_AutoscaleDisable(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--autoscale=false"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	as, ok := body["autoscale"].(map[string]any)
	if !ok {
		t.Fatalf("expected an autoscale object, got %v", body["autoscale"])
	}
	if as["enabled"] != false {
		t.Errorf("enabled: got %v", as["enabled"])
	}
	if _, present := as["min_replicas"]; present {
		t.Errorf("min_replicas should be absent when only disabling")
	}
	if _, present := as["target"]; present {
		t.Errorf("target should be absent when only disabling")
	}
}

// A single autoscale field can be changed on its own; the server merges it over
// the stored values, so the CLI sends only the changed key.
func TestAppsSet_AutoscaleTargetOnly(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--autoscale-target", "0.5"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	as, ok := body["autoscale"].(map[string]any)
	if !ok {
		t.Fatalf("expected an autoscale object, got %v", body["autoscale"])
	}
	if as["target"] != 0.5 {
		t.Errorf("target: got %v", as["target"])
	}
	if _, present := as["enabled"]; present {
		t.Errorf("enabled should be absent when only the target changes")
	}
}

func TestAppsSet_RejectsAutoscaleTargetOutOfRange(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	if _, err := execCLI(t, "apps", "set", "demo", "--autoscale-target", "1.5"); err == nil {
		t.Fatal("expected an error for an out-of-range autoscale target")
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no request to be sent on a validation error, got %d", len(*reqs))
	}
}

// TestAppsSet_TierPlacement sends a repeatable --tier flag as a placement object
// and omits the replicas key.
func TestAppsSet_TierPlacement(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--tier", "local=2", "--tier", "burst=1", "--yes"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	placement, ok := body["placement"].(map[string]any)
	if !ok {
		t.Fatalf("expected placement object, got %v (%T)", body["placement"], body["placement"])
	}
	if placement["local"] != float64(2) || placement["burst"] != float64(1) {
		t.Errorf("expected placement {local:2, burst:1}, got %v", placement)
	}
	if _, present := body["replicas"]; present {
		t.Errorf("expected replicas to be absent when --tier is used, got %v", body["replicas"])
	}
}

// TestAppsSet_TierAndReplicasConflict rejects --tier together with --replicas
// before any HTTP request is made.
func TestAppsSet_TierAndReplicasConflict(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--tier", "local=1", "--replicas", "2")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// TestAppsSet_RejectsInvalidTierFormat rejects a --tier value lacking name=count.
func TestAppsSet_RejectsInvalidTierFormat(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--tier", "local")
	if err == nil || !strings.Contains(err.Error(), "name=count") {
		t.Errorf("expected 'name=count' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// TestAppsSet_RejectsTierNonIntCount rejects a --tier value whose count is not a
// non-negative integer.
func TestAppsSet_RejectsTierNonIntCount(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--tier", "local=-1")
	if err == nil || !strings.Contains(err.Error(), "non-negative integer") {
		t.Errorf("expected 'non-negative integer' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

func TestAppsSet_RejectsReplicasZero(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--replicas", "0")
	if err == nil || !strings.Contains(err.Error(), ">= 1") {
		t.Errorf("expected '--replicas must be >= 1', got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// Passing -1 explicitly must be rejected with the documented range error, not
// silently swallowed as "not provided" (which previously produced a confusing
// "at least one flag is required" no-op).
func TestAppsSet_RejectsMaxSessionsNegativeOne(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--max-sessions-per-replica", "-1")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1000") {
		t.Errorf("expected 'between 0 and 1000' error for -1, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

func TestAppsSet_RejectsMaxSessionsOutOfRange(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--max-sessions-per-replica", "1001")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1000") {
		t.Errorf("expected 'between 0 and 1000' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

func TestAppsSet_RequiresAtLeastOneFlag(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo")
	if err == nil || !strings.Contains(err.Error(), "at least one flag") {
		t.Errorf("expected 'at least one flag' error, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests, got %d", len(*reqs))
	}
}

// TestAppsSet_MaxSessionsNegativeOneWithOtherFlags asserts -1 is rejected even
// when combined with a valid flag: the cap is determined by Flags().Changed,
// so an explicit out-of-range value is a real error rather than a swallowed
// sentinel.
func TestAppsSet_MaxSessionsNegativeOneWithOtherFlags(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--replicas", "2", "--max-sessions-per-replica", "-1")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1000") {
		t.Errorf("expected 'between 0 and 1000' error for -1, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// TestAppsSet_RejectsInvalidNegativeHibernateTimeout verifies that negative
// hibernate-timeout values other than -1 are rejected with a clear error.
func TestAppsSet_RejectsInvalidNegativeHibernateTimeout(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "apps", "set", "demo", "--hibernate-timeout", "-2")
	if err == nil {
		t.Fatal("expected error for --hibernate-timeout -2, got nil")
	}
	if !strings.Contains(err.Error(), "hibernate-timeout") {
		t.Errorf("error should mention hibernate-timeout, got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when validation fails, got %d", len(*reqs))
	}
}

// TestAppsLogs_NoFollow_PassesFollowFalseAndPrintsBody asserts that
// --no-follow:
//   - sends ?follow=false on the wire (so the server returns plain text and
//     closes the connection rather than starting an SSE stream), and
//   - in table mode (-o table) prints one line per server line.
func TestAppsLogs_NoFollow_PassesFollowFalseAndPrintsBody(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "alpha\nbeta\ngamma\n")

	// Pass -o table explicitly: tests run without a TTY so the default would be
	// NDJSON (streaming command piped). Table mode is the human-readable path.
	out, err := execCLI(t, "apps", "logs", "demo", "--tail", "50", "--no-follow", "-o", "table")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Path != "/api/apps/demo/logs" {
		t.Errorf("path = %q, want /api/apps/demo/logs", req.Path)
	}
	if !strings.Contains(req.Query, "tail=50") {
		t.Errorf("query missing tail=50: %q", req.Query)
	}
	if !strings.Contains(req.Query, "follow=false") {
		t.Errorf("query missing follow=false: %q", req.Query)
	}
	if got := out; got != "alpha\nbeta\ngamma\n" {
		t.Errorf("stdout = %q, want %q", got, "alpha\nbeta\ngamma\n")
	}
}

// TestAppsLogs_Tail_PassesTailParam asserts that --tail N alone (without
// --no-follow) still sends the param so the SSE initial-burst is bounded.
func TestAppsLogs_Tail_PassesTailParam(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "") // body is irrelevant; we only check the request

	// httpClient/http.DefaultClient.Do returns; with --no-follow false the CLI
	// would normally block in scanner.Scan on a long-lived SSE. Our httptest
	// server returns immediately after writing the (empty) body, so scanner
	// returns nil and the call completes cleanly.
	if _, err := execCLI(t, "apps", "logs", "demo", "--tail", "10"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if !strings.Contains((*reqs)[0].Query, "tail=10") {
		t.Errorf("query missing tail=10: %q", (*reqs)[0].Query)
	}
}

// TestAppsLogs_TailValidation rejects out-of-range --tail values before
// touching the network. Without this guard the server would reject the
// request with 400; surfacing the error early gives a cleaner CLI UX.
func TestAppsLogs_TailValidation(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	for _, badTail := range []string{"0", "-5", "10001"} {
		_, err := execCLI(t, "apps", "logs", "demo", "--tail", badTail)
		if err == nil {
			t.Errorf("tail=%s: expected error, got nil", badTail)
		}
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no requests on validation failure, got %d", len(*reqs))
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

	_, err := execCLI(t, "apps", "logs", "noapp")
	if err == nil {
		t.Fatal("expected non-nil error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should contain status code 404, got: %v", err)
	}
}

// FORMAT-6: apps logs piped without -o must emit NDJSON log objects (the
// streaming default for a piped context). Each line from the server is wrapped
// as {"line":"..."} so consumers can parse structured records. This uses
// httptest so http.DefaultClient (used for SSE) reaches a real server.
func TestAppsLogs_Piped_EmitsNDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: hello world\n\ndata: second line\n\n")
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// No -o flag: piped context resolves to NDJSON for streaming commands.
	out, err := execCLI(t, "apps", "logs", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}

	// Each log line must be a JSON object with a "line" key.
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		if raw == "" {
			continue
		}
		var obj map[string]any
		if jerr := json.Unmarshal([]byte(raw), &obj); jerr != nil {
			t.Fatalf("output line is not JSON: %v\nline=%q\nfull output=%q", jerr, raw, out)
		}
		if _, ok := obj["line"]; !ok {
			t.Errorf("NDJSON object missing 'line' key: %v", obj)
		}
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("log content missing from output; got: %q", out)
	}
}

// TestAppsStop sends a POST /api/apps/{slug}/stop and expects a clean message.
func TestAppsStop(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"stopped"}`)

	if _, err := execCLI(t, "apps", "stop", "demo"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" || req.Path != "/api/apps/demo/stop" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}
}

// TestAppsStop_ServerError ensures a non-2xx response is propagated as an error.
func TestAppsStop_ServerError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"not found"}`)

	_, err := execCLI(t, "apps", "stop", "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

// TestAppsStop_ServerErrorUnwrapped asserts the {"error":...} envelope is
// unwrapped into a clean message rather than dumped as raw JSON.
func TestAppsStop_ServerErrorUnwrapped(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"app is hibernating"}`)

	_, err := execCLI(t, "apps", "stop", "demo")
	if err == nil {
		t.Fatal("expected error for 409, got nil")
	}
	if !strings.Contains(err.Error(), "app is hibernating") {
		t.Errorf("error should surface the server message, got %q", err.Error())
	}
	if strings.Contains(err.Error(), `{"error"`) {
		t.Errorf("error should not contain the raw JSON envelope, got %q", err.Error())
	}
}

// TestAppsDelete_WithYesFlag tests deletion bypassing the confirmation prompt.
func TestAppsDelete_WithYesFlag(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")

	if _, err := execCLI(t, "apps", "delete", "demo", "--yes"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "DELETE" || req.Path != "/api/apps/demo" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}
}

// TestAppsDelete_WithConfirmation tests the interactive confirmation flow.
func TestAppsDelete_WithConfirmation(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")

	// The runAppsDelete tty gate refuses non-interactive callers without
	// --yes. Tests simulate the tty so the confirmation path runs.
	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	// Correct confirmation: user types the slug.
	if _, err := execCLIStdin(t, strings.NewReader("demo\n"), "apps", "delete", "demo"); err != nil {
		t.Fatalf("unexpected error with correct confirmation: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
}

// TestAppsDelete_WrongConfirmation ensures a wrong confirmation aborts without
// making any network call.
func TestAppsDelete_WrongConfirmation(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	_, err := execCLIStdin(t, strings.NewReader("wrong\n"), "apps", "delete", "demo")
	if err == nil {
		t.Fatal("expected error for wrong confirmation, got nil")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("error should mention 'aborted', got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when aborted, got %d", len(*reqs))
	}
}

// TestAppsDelete_NonTtyWithoutYesReturnsClearError pins the tty gate. Before
// the gate, `shinyhub apps delete demo < /dev/null` (a CI/cron pattern) hung
// on the prompt or surfaced a confusing "read confirmation: EOF". The fix
// must short-circuit with a confirmation_required error whose hint names
// --yes, and must NOT issue any DELETE request.
func TestAppsDelete_NonTtyWithoutYesReturnsClearError(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return false }

	_, err := execCLI(t, "apps", "delete", "demo")
	if err == nil {
		t.Fatal("expected non-tty error pointing at --yes, got nil")
	}
	kind, _ := classify(err)
	if kind != KindConfirmationRequired {
		t.Errorf("error kind = %q, want %q", kind, KindConfirmationRequired)
	}
	var he hintedError
	if !errors.As(err, &he) || !strings.Contains(he.Hint(), "--yes") {
		t.Errorf("hint must mention --yes so automation has a clear path, got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when refusing non-tty without --yes, got %d", len(*reqs))
	}
}

// TestAppsDelete_PromptGoesToStderr pins that the destructive-confirmation
// prompt is written to stderr so `shinyhub apps delete foo | tee log` keeps
// stdout clean for the success line.
func TestAppsDelete_PromptGoesToStderr(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, "")

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	// execCLIStdin captures combined stdout+stderr via forceWriters; the prompt
	// text must appear somewhere in the combined output (it is written to stderr
	// by the command, which forceWriters merges into the single capture buffer).
	out, err := execCLIStdin(t, strings.NewReader("demo\n"), "apps", "delete", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "permanently delete app") {
		t.Errorf("destructive prompt should land on stderr; got %q", out)
	}
}

// TestAppsDeployments lists deployment history.
// Non-TTY output is the bounded JSON envelope; table rendering is preserved
// verbatim inside the tableFn closure in runAppsDeployments.
func TestAppsDeployments(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"id":3,"version":"1735689600000","status":"active","created_at":"2026-01-01T00:00:00Z"},{"id":1,"version":"1735600000000","status":"active","created_at":"2025-12-31T00:00:00Z"}]`)

	out, err := execCLI(t, "apps", "deployments", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Non-TTY output is the standard envelope.
	if !strings.Contains(out, `"items"`) {
		t.Errorf("expected envelope with items, got: %q", out)
	}
	if !strings.Contains(out, `"total"`) {
		t.Errorf("expected envelope with total, got: %q", out)
	}
}

// TestAppsStart sends a POST /api/apps/{slug}/restart and reports "started"
// instead of "restarted" so the verb in the output matches what the user typed.
func TestAppsStart(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"running"}`)

	out, err := execCLI(t, "apps", "start", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" || req.Path != "/api/apps/demo/restart" {
		t.Errorf("expected POST /api/apps/demo/restart, got %s %s", req.Method, req.Path)
	}
	// In piped mode the output is a JSON envelope; verify the success envelope
	// contains the slug and the running status.
	if !strings.Contains(out, `"slug"`) || !strings.Contains(out, `"running"`) {
		t.Errorf("expected JSON envelope with slug and running status, got %q", out)
	}
}

// TestAppsStart_ServerError ensures a non-2xx response propagates as an error.
func TestAppsStart_ServerError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"app has no successful deployment - see: shinyhub apps deployments fresh"}`)

	_, err := execCLI(t, "apps", "start", "fresh")
	if err == nil {
		t.Fatal("expected error for 409, got nil")
	}
	if !strings.Contains(err.Error(), "no successful deployment") {
		t.Errorf("error should surface the server message, got: %v", err)
	}
}

// TestAppsShow renders the app envelope returned by GET /api/apps/<slug>.
// The test pins the field labels so accidental rewordings break loudly.
func TestAppsShow(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo App","owner_id":7,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":15,"deploy_count":3,"hibernate_timeout_minutes":null,"memory_limit_mb":512,"cpu_quota_percent":100,"created_at":"2026-04-25T10:00:00Z","updated_at":"2026-04-25T11:00:00Z"},"replicas_status":[{"index":0,"status":"running","pid":1234,"port":34567},{"index":1,"status":"running","pid":1235,"port":34568}]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 || (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/api/apps/demo" {
		t.Fatalf("expected GET /api/apps/demo, got %+v", *reqs)
	}
	for _, want := range []string{
		"Slug:        demo",
		"Name:        Demo App",
		"Status:      running",
		"Access:      private",
		"Deploys:     3",
		"Replicas:    2",
		"Max sess/r:  15",
		"Hibernate:   (global default)",
		"Memory:      512 MB",
		"CPU:         100%",
		"INDEX  STATUS",
		"1234",
		"34567",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\nfull output:\n%s", want, out)
		}
	}
}

// DEP-5: when an app's per-replica cap is 0 (inherit), `apps show` must
// annotate it with the resolved runtime default and print the admission ceiling
// (replicas × effective cap) instead of a bare, cryptic "0".
func TestAppsShow_InheritedCapShowsRuntimeDefaultAndCeiling(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":0,"deploy_count":1},"effective_max_sessions_per_replica":10,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "runtime default: 10") {
		t.Errorf("expected the 0 cap to be annotated with the runtime default, got:\n%s", out)
	}
	if !strings.Contains(out, "Admission ceiling: 2 × 10 = 20") {
		t.Errorf("expected admission ceiling line (2 × 10 = 20), got:\n%s", out)
	}
}

// DEP-5: an explicit per-replica cap prints the admission ceiling from that cap.
func TestAppsShow_ExplicitCapShowsCeiling(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"private","status":"running","replicas":3,"max_sessions_per_replica":5,"deploy_count":1},"effective_max_sessions_per_replica":5,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Admission ceiling: 3 × 5 = 15") {
		t.Errorf("expected admission ceiling line (3 × 5 = 15), got:\n%s", out)
	}
}

// DEP-5: when the effective cap resolves to 0, sessions are unlimited; say so
// rather than printing "ceiling = 0".
func TestAppsShow_UnlimitedCap(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":0,"deploy_count":1},"effective_max_sessions_per_replica":0,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "unlimited") {
		t.Errorf("expected an 'unlimited' admission ceiling, got:\n%s", out)
	}
}

// CR2-4: an older server may not return effective_max_sessions_per_replica.
// With an explicit nonzero per-app cap, the CLI must not decode the absent
// field as 0 and report "unlimited"; it falls back to the app cap for the
// ceiling.
func TestAppsShow_MissingEffectiveCapFallsBackToAppCap(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":7,"deploy_count":1},"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "unlimited") {
		t.Errorf("explicit cap 7 must not read as unlimited when the server omits the effective cap, got:\n%s", out)
	}
	if !strings.Contains(out, "Admission ceiling: 2 × 7 = 14") {
		t.Errorf("expected the ceiling to fall back to the app cap (2 × 7 = 14), got:\n%s", out)
	}
}

// CR2-4: when the server omits the effective cap AND the app inherits (cap 0),
// the ceiling cannot be resolved client-side; the CLI must not guess
// "unlimited". It omits the ceiling line rather than printing a false one.
func TestAppsShow_MissingEffectiveCapInheritedOmitsCeiling(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":0,"deploy_count":1},"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "unlimited") {
		t.Errorf("must not claim unlimited when the effective cap is unknown, got:\n%s", out)
	}
	if strings.Contains(out, "Admission ceiling:") {
		t.Errorf("ceiling is unresolvable here and should be omitted, got:\n%s", out)
	}
}

// TestAppsShow_JSON passes through the raw envelope when --json is set.
func TestAppsShow_JSON(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	body := `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"public","status":"running","replicas":1,"max_sessions_per_replica":10,"deploy_count":1},"replicas_status":[]}`
	setResp(200, body)

	out, err := execCLI(t, "apps", "show", "demo", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out = strings.TrimSpace(out)
	if out != body {
		t.Errorf("--json should pass body through verbatim\n got: %q\nwant: %q", out, body)
	}
}

// TestAppsShow_NotFound surfaces a 404 as a non-zero exit with the server
// message attached so scripts can branch on missing apps.
func TestAppsShow_NotFound(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"app not found"}`)

	_, err := execCLI(t, "apps", "show", "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should include status 404, got %v", err)
	}
}

// TestTokensList lists API tokens.
func TestTokensList(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"id":1,"name":"ci-token","created_at":"2026-01-01T00:00:00Z"}]`)

	out, err := execCLI(t, "tokens", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "ci-token") {
		t.Errorf("expected output to contain 'ci-token', got: %q", out)
	}
}

// TestTokensRevoke sends a DELETE request to revoke a token by ID.
func TestTokensRevoke(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")

	if _, err := execCLI(t, "tokens", "revoke", "42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "DELETE" || req.Path != "/api/tokens/42" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}
}

// TestTokensCreate_JSON verifies that --format json produces parseable JSON with
// all required fields.
func TestTokensCreate_JSON(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":7,"name":"ci","token":"shk_abcdef1234567890","created_at":"2026-05-12T15:04:05Z"}`)

	out, err := execCLI(t, "tokens", "create", "--name", "ci", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if (*reqs)[0].Method != "POST" || (*reqs)[0].Path != "/api/tokens" {
		t.Errorf("unexpected %s %s", (*reqs)[0].Method, (*reqs)[0].Path)
	}

	out = strings.TrimSpace(out)
	var result struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Token     string `json:"token"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out)
	}
	if result.ID != 7 {
		t.Errorf("expected id=7, got %d", result.ID)
	}
	if result.Name != "ci" {
		t.Errorf("expected name=%q, got %q", "ci", result.Name)
	}
	if result.Token != "shk_abcdef1234567890" {
		t.Errorf("expected token=%q, got %q", "shk_abcdef1234567890", result.Token)
	}
	if result.CreatedAt != "2026-05-12T15:04:05Z" {
		t.Errorf("expected created_at=%q, got %q", "2026-05-12T15:04:05Z", result.CreatedAt)
	}
}

// TestTokensCreate_TextDefault verifies that omitting --format (or using
// --format text) produces the human-readable output.
func TestTokensCreate_TextDefault(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"id":3,"name":"mytoken","token":"shk_xyz","created_at":"2026-05-12T10:00:00Z"}`)

	out, err := execCLI(t, "tokens", "create", "--name", "mytoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "API token: shk_xyz") {
		t.Errorf("expected human-readable 'API token:' line, got: %q", out)
	}
	if !strings.Contains(out, "Store this") {
		t.Errorf("expected 'Store this' reminder line, got: %q", out)
	}
	// Must not be JSON.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("default output should not be JSON, got: %q", out)
	}
}

// TestTokensCreate_FormatBogus verifies that an unrecognised --format value
// returns an error before making any HTTP request.
func TestTokensCreate_FormatBogus(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "tokens", "create", "--name", "ci", "--format", "yaml")
	if err == nil {
		t.Fatal("expected error for --format yaml, got nil")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error should mention the bad format value, got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests for invalid format, got %d", len(*reqs))
	}
}

// TestTokensCreate_FormatTextConflictsWithOutputJson verifies that the
// resolveLegacyTextJSON conflict path works: --format text (force table mode)
// combined with -o json (force JSON) is a validation error, not silent
// override of one flag by the other.
func TestTokensCreate_FormatTextConflictsWithOutputJson(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "tokens", "create", "--name", "ci", "--format", "text", "-o", "json")
	if err == nil {
		t.Fatal("want error for --format text -o json conflict, got nil")
	}
	if code := exitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1 (validation)", code)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests on conflict error, got %d", len(*reqs))
	}
}

// TestTokensRevoke_ByName_OneMatch verifies that --name resolves to the correct
// token ID and issues a single DELETE request.
func TestTokensRevoke_ByName_OneMatch(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	// The test server returns the same body for both GET /api/tokens (list) and
	// DELETE /api/tokens/42. The DELETE body is ignored; we care about the path.
	setResp(200, `[{"id":42,"name":"ci","created_at":"2026-05-01T00:00:00Z"}]`)

	if _, err := execCLI(t, "tokens", "revoke", "--name", "ci"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests (list + delete), got %d", len(*reqs))
	}
	if (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/api/tokens" {
		t.Errorf("expected GET /api/tokens first, got %s %s", (*reqs)[0].Method, (*reqs)[0].Path)
	}
	if (*reqs)[1].Method != "DELETE" || (*reqs)[1].Path != "/api/tokens/42" {
		t.Errorf("expected DELETE /api/tokens/42, got %s %s", (*reqs)[1].Method, (*reqs)[1].Path)
	}
}

// TestTokensRevoke_ByName_NoMatch verifies that a missing name returns a clear
// "no token named" error without issuing a DELETE.
func TestTokensRevoke_ByName_NoMatch(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":1,"name":"other","created_at":"2026-05-01T00:00:00Z"}]`)

	_, err := execCLI(t, "tokens", "revoke", "--name", "missing")
	if err == nil {
		t.Fatal("expected error for non-existent name, got nil")
	}
	if !strings.Contains(err.Error(), `no token named`) {
		t.Errorf("error should say 'no token named', got: %v", err)
	}
	// Only the list request should have been made; no DELETE.
	deleteSeen := false
	for _, r := range *reqs {
		if r.Method == "DELETE" {
			deleteSeen = true
		}
	}
	if deleteSeen {
		t.Errorf("expected no DELETE request when name not found, but one was issued")
	}
}

// TestTokensRevoke_ByName_MultipleMatches verifies that duplicate names produce
// an error pointing users toward revoke-by-id.
func TestTokensRevoke_ByName_MultipleMatches(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":1,"name":"ci","created_at":"2026-05-01T00:00:00Z"},{"id":2,"name":"ci","created_at":"2026-05-02T00:00:00Z"}]`)

	_, err := execCLI(t, "tokens", "revoke", "--name", "ci")
	if err == nil {
		t.Fatal("expected error for multiple matching names, got nil")
	}
	if !strings.Contains(err.Error(), "multiple tokens named") {
		t.Errorf("error should say 'multiple tokens named', got: %v", err)
	}
	if !strings.Contains(err.Error(), "revoke by id") {
		t.Errorf("error should suggest 'revoke by id', got: %v", err)
	}
	deleteSeen := false
	for _, r := range *reqs {
		if r.Method == "DELETE" {
			deleteSeen = true
		}
	}
	if deleteSeen {
		t.Errorf("expected no DELETE request for ambiguous name, but one was issued")
	}
}

// TestTokensRevoke_BothIDAndName verifies that supplying both a positional ID
// and --name returns a mutual-exclusion error before any HTTP request.
func TestTokensRevoke_BothIDAndName(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "tokens", "revoke", "42", "--name", "ci")
	if err == nil {
		t.Fatal("expected mutual-exclusion error, got nil")
	}
	if !strings.Contains(err.Error(), "specify either id or --name") {
		t.Errorf("error should say 'specify either id or --name', got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests for mutual-exclusion error, got %d", len(*reqs))
	}
}

// ── apps set: replica-change confirm guard + --wait (DEP-2) ──────────────────

// Changing replicas restarts the app and drops active sessions, so an
// interactive caller must confirm. Declining must abort before any PATCH.
func TestAppsSet_ReplicaChange_TTYAbortsOnNo(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	_, err := execCLIStdin(t, strings.NewReader("n\n"), "apps", "set", "demo", "--replicas", "3")
	if err == nil {
		t.Fatal("expected abort error when user declines, got nil")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("error should mention 'aborted', got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when replica change is declined, got %d", len(*reqs))
	}
}

// Confirming the prompt with "y" must proceed with the PATCH.
func TestAppsSet_ReplicaChange_TTYProceedsOnYes(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	if _, err := execCLIStdin(t, strings.NewReader("y\n"), "apps", "set", "demo", "--replicas", "3"); err != nil {
		t.Fatalf("unexpected error after confirming: %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Method != "PATCH" {
		t.Fatalf("expected one PATCH after confirmation, got %#v", *reqs)
	}
}

// --yes bypasses the prompt even on a TTY.
func TestAppsSet_ReplicaChange_YesFlagSkipsPrompt(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	if _, err := execCLI(t, "apps", "set", "demo", "--replicas", "3", "--yes"); err != nil {
		t.Fatalf("unexpected error with --yes: %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Method != "PATCH" {
		t.Fatalf("expected one PATCH with --yes, got %#v", *reqs)
	}
}

// Non-TTY without --yes must refuse with confirmation_required and make no
// network call. Automation that wants to scale via the CLI must pass --yes
// explicitly; this makes the restart side-effect an intentional opt-in.
func TestAppsSet_ReplicaChange_NonTTYRefusesWithoutYes(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return false }

	_, err := execCLI(t, "apps", "set", "demo", "--replicas", "3")
	if err == nil {
		t.Fatal("expected confirmation_required error on non-TTY without --yes, got nil")
	}
	kind, code := classify(err)
	if kind != KindConfirmationRequired {
		t.Errorf("classify(err).kind = %q, want %q", kind, KindConfirmationRequired)
	}
	if code != 1 {
		t.Errorf("classify(err).code = %d, want 1", code)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no PATCH before the refusal, got %d requests", len(*reqs))
	}
}

// A hot setting (hibernate-timeout, cap) does not restart the app, so it must
// never trigger the replica confirm even on an interactive terminal.
func TestAppsSet_HibernateChange_NoPromptOnTTY(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	// Empty stdin: if the command tried to prompt it would fail on read.
	if _, err := execCLIStdin(t, strings.NewReader(""), "apps", "set", "demo", "--hibernate-timeout", "10"); err != nil {
		t.Fatalf("unexpected error for hot setting change: %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Method != "PATCH" {
		t.Fatalf("expected one PATCH for hibernate change, got %#v", *reqs)
	}
}

// --wait makes the command poll GET /api/apps/{slug} until the app is running
// again after the replica change.
func TestAppsSet_Wait_PollsUntilRunning(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"app":{"status":"running"}}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--replicas", "3", "--yes", "--wait"); err != nil {
		t.Fatalf("unexpected error with --wait: %v", err)
	}
	sawPatch, sawGet := false, false
	for _, r := range *reqs {
		switch r.Method {
		case "PATCH":
			sawPatch = true
		case "GET":
			if r.Path == "/api/apps/demo" {
				sawGet = true
			}
		}
	}
	if !sawPatch || !sawGet {
		t.Errorf("expected both PATCH and a GET poll of /api/apps/demo, got %#v", *reqs)
	}
}

// A replica change kicks off an async server-side redeploy while the app row
// still reports "running", so the first poll observing status:"running" is not
// proof the new pool is up. The server advertises redeploy_in_flight while the
// pool is cycling; --wait must keep polling until it clears rather than
// returning on the stale "running".
func TestAppsSet_Wait_RedeployInFlightBlocksReady(t *testing.T) {
	var getCount int32
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/apps/demo" {
			if atomic.AddInt32(&getCount, 1) == 1 {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"app":{"status":"running"},"redeploy_in_flight":true}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"status":"running"},"redeploy_in_flight":false}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})

	orig := healthPollInterval
	t.Cleanup(func() { healthPollInterval = orig })
	healthPollInterval = time.Millisecond

	if _, err := execCLI(t, "apps", "set", "demo", "--replicas", "3", "--yes", "--wait"); err != nil {
		t.Fatalf("unexpected error with --wait: %v", err)
	}
	if got := atomic.LoadInt32(&getCount); got < 2 {
		t.Fatalf("wait returned on the first redeploying poll: got %d GET polls, want >= 2", got)
	}
}

// TestAppsRestart_WaitPolls verifies that `apps restart --wait` blocks on the
// health poll until the app reports running, rather than returning the moment
// the restart request is accepted.
func TestAppsRestart_WaitPolls(t *testing.T) {
	var getCount int32
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/restart":
			_, _ = w.Write([]byte(`{"status":"running","slug":"demo"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			if atomic.AddInt32(&getCount, 1) < 2 {
				_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
			} else {
				_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
			}
		default:
			w.WriteHeader(404)
		}
	})
	orig := healthPollInterval
	t.Cleanup(func() { healthPollInterval = orig })
	healthPollInterval = time.Millisecond

	if _, err := execCLI(t, "apps", "restart", "demo", "--wait"); err != nil {
		t.Fatalf("restart --wait error: %v", err)
	}
	if got := atomic.LoadInt32(&getCount); got < 2 {
		t.Fatalf("expected --wait to poll until running (>=2 GETs), got %d", got)
	}
}

// TestAppsRollback_WaitPolls verifies the same for `apps rollback --wait`.
func TestAppsRollback_WaitPolls(t *testing.T) {
	var getCount int32
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/rollback":
			_, _ = w.Write([]byte(`{"status":"rolled_back","slug":"demo"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			if atomic.AddInt32(&getCount, 1) < 2 {
				_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
			} else {
				_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
			}
		default:
			w.WriteHeader(404)
		}
	})
	orig := healthPollInterval
	t.Cleanup(func() { healthPollInterval = orig })
	healthPollInterval = time.Millisecond

	if _, err := execCLI(t, "apps", "rollback", "demo", "--wait"); err != nil {
		t.Fatalf("rollback --wait error: %v", err)
	}
	if got := atomic.LoadInt32(&getCount); got < 2 {
		t.Fatalf("expected --wait to poll until running (>=2 GETs), got %d", got)
	}
}

func TestAppsShow_RendersRejectsByReason(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":1,"max_sessions_per_replica":1,"deploy_count":1,"hibernate_timeout_minutes":null,"created_at":"2026-04-25T10:00:00Z","updated_at":"2026-04-25T11:00:00Z"},"effective_max_sessions_per_replica":1,"replicas_status":[],"rejects_by_reason":{"window_seconds":600,"counts":{"pool-saturated":4103,"app-not-ready":12}}}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"rejects (last 10m):",
		"  pool-saturated: 4103",
		"  app-not-ready: 12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\nfull output:\n%s", want, out)
		}
	}
}

// TestAppsShow_RendersLostReplicaReason shows the derived per-replica reason
// (e.g. "worker unavailable") alongside a lost replica so the degraded state is
// disambiguated at a glance.
func TestAppsShow_RendersLostReplicaReason(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"degraded","replicas":2,"max_sessions_per_replica":1,"deploy_count":1},"effective_max_sessions_per_replica":1,"replicas_status":[{"index":0,"status":"running","pid":1234,"port":34567},{"index":1,"status":"lost","reason":"worker unavailable"}]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "worker unavailable") {
		t.Errorf("expected lost replica reason in output\nfull output:\n%s", out)
	}
	// The running replica must not gain a spurious reason annotation.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "1234") && strings.Contains(line, "worker unavailable") {
			t.Errorf("running replica should have no reason annotation: %q", line)
		}
	}
}

// TestAppsShow_RendersAutoscaleEnabled pins the autoscale summary line so an
// operator can read the bounds and effective target at a glance.
func TestAppsShow_RendersAutoscaleEnabled(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":10,"deploy_count":1,"autoscale_enabled":true,"autoscale_min_replicas":1,"autoscale_max_replicas":4,"autoscale_target":0.7},"effective_autoscale_target":0.7,"effective_max_sessions_per_replica":10,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Autoscale:   on (replicas 1-4, target 70%)") {
		t.Errorf("expected autoscale summary line, got:\n%s", out)
	}
}

// When an app's own target is 0 it inherits the runtime default; show resolves
// the effective target the server reports rather than printing a bare 0%.
func TestAppsShow_RendersAutoscaleInheritedTarget(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":10,"deploy_count":1,"autoscale_enabled":true,"autoscale_min_replicas":2,"autoscale_max_replicas":6,"autoscale_target":0},"effective_autoscale_target":0.8,"effective_max_sessions_per_replica":10,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Autoscale:   on (replicas 2-6, target 80%)") {
		t.Errorf("expected autoscale line with the inherited effective target, got:\n%s", out)
	}
}

func TestAppsShow_RendersAutoscaleOff(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":10,"deploy_count":1,"autoscale_enabled":false},"effective_max_sessions_per_replica":10,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Autoscale:   off") {
		t.Errorf("expected autoscale off line, got:\n%s", out)
	}
}

// TestAppsShow_OmitsRejectsWhenAbsent guards against the rejects section
// rendering when the server sends no rejects_by_reason block.
func TestAppsShow_OmitsRejectsWhenAbsent(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":1,"max_sessions_per_replica":1,"deploy_count":1,"hibernate_timeout_minutes":null,"created_at":"2026-04-25T10:00:00Z","updated_at":"2026-04-25T11:00:00Z"},"effective_max_sessions_per_replica":1,"replicas_status":[]}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "rejects (") {
		t.Errorf("rejects section should be omitted when server sends no rejects_by_reason block\nfull output:\n%s", out)
	}
}

// TestAppsSet_MinWarmReplicas sends --min-warm-replicas 2 and asserts the PATCH
// body contains min_warm_replicas=2 and no other fields.
func TestAppsSet_MinWarmReplicas(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--min-warm-replicas", "2"); err != nil {
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
	if got := body["min_warm_replicas"]; got != float64(2) {
		t.Errorf("expected min_warm_replicas=2, got %v (%T)", got, got)
	}
	if _, present := body["replicas"]; present {
		t.Errorf("expected replicas to be absent when only min_warm_replicas changed")
	}
	if _, present := body["max_sessions_per_replica"]; present {
		t.Errorf("expected max_sessions_per_replica to be absent when only min_warm_replicas changed")
	}
}
