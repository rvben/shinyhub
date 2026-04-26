package cli

import (
	"bytes"
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

// TestAppsSet_MaxSessionsSentinelMinusOne verifies that explicitly passing -1
// (the flag's own default) is treated as "not provided" and does not trigger
// the range validator.
func TestAppsSet_MaxSessionsSentinelMinusOne(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	resetAppsSetFlags(t)

	// -1 is the cobra default for max-sessions-per-replica; if it were treated
	// as a real value it would fail the 0..1000 validator. Passing it together
	// with --replicas should succeed and not include max_sessions_per_replica in
	// the payload.
	appsCmd.SetArgs([]string{"set", "demo", "--replicas", "2", "--max-sessions-per-replica", "-1"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, present := body["max_sessions_per_replica"]; present {
		t.Errorf("max_sessions_per_replica should be absent when -1 sentinel is passed, got %v", body["max_sessions_per_replica"])
	}
}

// TestAppsSet_RejectsInvalidNegativeHibernateTimeout verifies that negative
// hibernate-timeout values other than -1 are rejected with a clear error.
func TestAppsSet_RejectsInvalidNegativeHibernateTimeout(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	resetAppsSetFlags(t)

	appsCmd.SetArgs([]string{"set", "demo", "--hibernate-timeout", "-2"})
	err := appsCmd.Execute()
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

// TestAppsStop sends a POST /api/apps/{slug}/stop and expects a clean message.
func TestAppsStop(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"stopped"}`)

	appsCmd.SetArgs([]string{"stop", "demo"})
	if err := appsCmd.Execute(); err != nil {
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

	appsCmd.SetArgs([]string{"stop", "missing"})
	err := appsCmd.Execute()
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

// TestAppsDelete_WithYesFlag tests deletion bypassing the confirmation prompt.
func TestAppsDelete_WithYesFlag(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")
	appsDeleteFlags.yes = false // reset

	appsCmd.SetArgs([]string{"delete", "demo", "--yes"})
	if err := appsCmd.Execute(); err != nil {
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
	appsDeleteFlags.yes = false

	// The runAppsDelete tty gate refuses non-interactive callers without
	// --yes. Tests simulate the tty so the confirmation path runs.
	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	// Correct confirmation: user types the slug.
	appsDeleteCmd.SetIn(strings.NewReader("demo\n"))
	appsCmd.SetArgs([]string{"delete", "demo"})
	if err := appsCmd.Execute(); err != nil {
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
	appsDeleteFlags.yes = false

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	appsDeleteCmd.SetIn(strings.NewReader("wrong\n"))
	appsCmd.SetArgs([]string{"delete", "demo"})
	err := appsCmd.Execute()
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
// must short-circuit with an error pointing at `--yes` and must NOT issue any
// DELETE request.
func TestAppsDelete_NonTtyWithoutYesReturnsClearError(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")
	appsDeleteFlags.yes = false

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return false }

	appsCmd.SetArgs([]string{"delete", "demo"})
	err := appsCmd.Execute()
	if err == nil {
		t.Fatal("expected non-tty error pointing at --yes, got nil")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should mention `--yes` so automation has a clear path, got: %v", err)
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
	appsDeleteFlags.yes = false

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return true }

	var stderr bytes.Buffer
	appsDeleteCmd.SetIn(strings.NewReader("demo\n"))
	appsDeleteCmd.SetErr(&stderr)
	t.Cleanup(func() { appsDeleteCmd.SetErr(nil) })

	appsCmd.SetArgs([]string{"delete", "demo"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stderr.String(), "permanently delete app") {
		t.Errorf("destructive prompt should land on stderr; got %q", stderr.String())
	}
}

// TestAppsDeployments lists deployment history.
func TestAppsDeployments(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"id":3,"version":"1735689600000","status":"active","created_at":"2026-01-01T00:00:00Z"},{"id":1,"version":"1735600000000","status":"active","created_at":"2025-12-31T00:00:00Z"}]`)
	// Reset json flag.
	appsDeploymentsFlags.jsonOutput = false
	for _, name := range []string{"json"} {
		f := appsDeploymentsCmd.Flags().Lookup(name)
		if f != nil {
			f.Changed = false
		}
	}

	var buf strings.Builder
	appsDeploymentsCmd.SetOut(&buf)
	appsCmd.SetArgs([]string{"deployments", "demo"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "ID") {
		t.Errorf("expected header row with ID, got: %q", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("expected deployment ID 3, got: %q", out)
	}
}

// TestAppsStart sends a POST /api/apps/{slug}/restart and reports "started"
// instead of "restarted" so the verb in the output matches what the user typed.
func TestAppsStart(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"slug":"demo","status":"running"}`)

	var buf strings.Builder
	appsStartCmd.SetOut(&buf)
	appsCmd.SetArgs([]string{"start", "demo"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" || req.Path != "/api/apps/demo/restart" {
		t.Errorf("expected POST /api/apps/demo/restart, got %s %s", req.Method, req.Path)
	}
	if got := buf.String(); !strings.Contains(got, "demo: started") {
		t.Errorf("expected output to contain 'demo: started', got %q", got)
	}
	if strings.Contains(buf.String(), "restarted") {
		t.Errorf("output should say 'started', not 'restarted', got %q", buf.String())
	}
}

// TestAppsStart_ServerError ensures a non-2xx response propagates as an error.
func TestAppsStart_ServerError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"app has never been deployed"}`)

	appsCmd.SetArgs([]string{"start", "fresh"})
	err := appsCmd.Execute()
	if err == nil {
		t.Fatal("expected error for 409, got nil")
	}
	if !strings.Contains(err.Error(), "never been deployed") {
		t.Errorf("error should surface the server message, got: %v", err)
	}
}

// TestAppsShow renders the app envelope returned by GET /api/apps/<slug>.
// The test pins the field labels so accidental rewordings break loudly.
func TestAppsShow(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo App","owner_id":7,"access":"private","status":"running","replicas":2,"max_sessions_per_replica":15,"deploy_count":3,"hibernate_timeout_minutes":null,"memory_limit_mb":512,"cpu_quota_percent":100,"created_at":"2026-04-25T10:00:00Z","updated_at":"2026-04-25T11:00:00Z"},"replicas_status":[{"index":0,"status":"running","pid":1234,"port":34567},{"index":1,"status":"running","pid":1235,"port":34568}]}`)
	appsShowFlags.jsonOutput = false

	var buf strings.Builder
	appsShowCmd.SetOut(&buf)
	appsCmd.SetArgs([]string{"show", "demo"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 || (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/api/apps/demo" {
		t.Fatalf("expected GET /api/apps/demo, got %+v", *reqs)
	}
	out := buf.String()
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

// TestAppsShow_JSON passes through the raw envelope when --json is set.
func TestAppsShow_JSON(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	body := `{"app":{"slug":"demo","name":"Demo","owner_id":1,"access":"public","status":"running","replicas":1,"max_sessions_per_replica":10,"deploy_count":1},"replicas_status":[]}`
	setResp(200, body)
	appsShowFlags.jsonOutput = false

	var buf strings.Builder
	appsShowCmd.SetOut(&buf)
	appsCmd.SetArgs([]string{"show", "demo", "--json"})
	if err := appsCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if out != body {
		t.Errorf("--json should pass body through verbatim\n got: %q\nwant: %q", out, body)
	}
}

// TestAppsShow_NotFound surfaces a 404 as a non-zero exit with the server
// message attached so scripts can branch on missing apps.
func TestAppsShow_NotFound(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"app not found"}`)
	appsShowFlags.jsonOutput = false

	appsCmd.SetArgs([]string{"show", "missing"})
	err := appsCmd.Execute()
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
	tokensListFlags.jsonOutput = false
	for _, name := range []string{"json"} {
		f := tokensListCmd.Flags().Lookup(name)
		if f != nil {
			f.Changed = false
		}
	}

	var buf strings.Builder
	tokensListCmd.SetOut(&buf)
	tokensCmd.SetArgs([]string{"list"})
	if err := tokensCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "ci-token") {
		t.Errorf("expected output to contain 'ci-token', got: %q", out)
	}
}

// TestTokensRevoke sends a DELETE request to revoke a token by ID.
func TestTokensRevoke(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, "")

	tokensCmd.SetArgs([]string{"revoke", "42"})
	if err := tokensCmd.Execute(); err != nil {
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

