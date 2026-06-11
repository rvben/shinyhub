package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── apps start (if_not_running) ──────────────────────────────────────────────

// TestAppsStart_AlreadyRunning_ExitsZeroWithNote verifies that `apps start`
// on a running app exits 0 and renders a no-op message, not an error. The CLI
// sends ?if_not_running=true; the server returns 200 {"status":"running","note":
// "already running"} without cycling the pool.
func TestAppsStart_AlreadyRunning_ExitsZeroWithNote(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/apps/demo/restart" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("if_not_running") != "true" {
			t.Errorf("apps start did not send ?if_not_running=true")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"running","note":"already running"}`)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "start", "demo", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("apps start already-running: unexpected error: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout is not JSON: %s", out.String())
	}
	if obj["status"] != "running" {
		t.Errorf("status = %v, want running", obj["status"])
	}
	if obj["note"] != "already running" {
		t.Errorf("note = %v, want 'already running'", obj["note"])
	}
}

// TestAppsStart_SendsIfNotRunningParam verifies that `apps start` adds
// ?if_not_running=true to the restart URL, distinguishing it from `apps restart`.
func TestAppsStart_SendsIfNotRunningParam(t *testing.T) {
	resetFormatState(t)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"slug":"demo","status":"running"}`)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"apps", "start", "demo"})
	_ = root.Execute()

	if !strings.Contains(gotQuery, "if_not_running=true") {
		t.Errorf("apps start did not send if_not_running=true; query = %q", gotQuery)
	}
}

// TestAppsRestart_DoesNotSendIfNotRunning verifies that `apps restart` does NOT
// send ?if_not_running=true, preserving always-cycle semantics.
func TestAppsRestart_DoesNotSendIfNotRunning(t *testing.T) {
	resetFormatState(t)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"slug":"demo","status":"running"}`)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"apps", "restart", "demo"})
	_ = root.Execute()

	if strings.Contains(gotQuery, "if_not_running") {
		t.Errorf("apps restart should NOT send if_not_running; query = %q", gotQuery)
	}
}

// ── apps stop (idempotent) ──────────────────────────────────────────────────

// TestAppsStop_AlreadyStopped_ExitsZero verifies that stop-on-stopped exits 0.
// The server always returns 200 for stop (handler is already idempotent);
// the CLI must not treat it as an error.
func TestAppsStop_AlreadyStopped_ExitsZero(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"slug":"demo","status":"stopped"}`)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "stop", "demo", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("apps stop already-stopped: unexpected error: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout not JSON: %s", out.String())
	}
	if obj["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", obj["status"])
	}
}

// ── apps delete (absent = exit 0) ──────────────────────────────────────────

// TestAppsDelete_Missing_ExitsZeroWithAbsentStatus verifies that deleting a
// non-existent app exits 0, renders status="absent", and writes a stderr note.
func TestAppsDelete_Missing_ExitsZeroWithAbsentStatus(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/apps/gone" {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"apps", "delete", "--yes", "gone", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("delete missing app should exit 0, got: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout not JSON: %s", out.String())
	}
	if obj["status"] != "absent" {
		t.Errorf("status = %v, want absent", obj["status"])
	}
	if obj["slug"] != "gone" {
		t.Errorf("slug = %v, want gone", obj["slug"])
	}
	if !strings.Contains(errBuf.String(), "gone") {
		t.Errorf("stderr should mention the slug; got: %q", errBuf.String())
	}
}

// writeJSONError writes a {"error": msg} JSON response - mirrors the server helper.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// ── apps access revoke (absent = exit 0) ───────────────────────────────────

// TestAppsAccessRevoke_NonMember_ExitsZero verifies that revoking access for a
// user who is not a member exits 0. The server returns 204 (idempotent); the
// CLI renders "absent" semantics.
func TestAppsAccessRevoke_NonMember_ExitsZero(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "access", "revoke", "my-app", "stranger"})
	if err := root.Execute(); err != nil {
		t.Fatalf("revoke non-member should exit 0, got: %v", err)
	}
}

// ── share add (duplicate mount = exit 0) ───────────────────────────────────

// TestShareAdd_DuplicateMount_ExitsZero verifies that a duplicate `share add`
// (server returns 200 no-op) exits 0 and renders the mounted action.
func TestShareAdd_DuplicateMount_ExitsZero(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "shared-data") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK) // no-op response
			fmt.Fprint(w, `{"source_slug":"src-app","source_id":2}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"share", "add", "my-app", "--from", "src-app", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("share add duplicate should exit 0, got: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout not JSON: %s", out.String())
	}
	if obj["status"] != "mounted" {
		t.Errorf("status = %v, want mounted", obj["status"])
	}
}

// TestShareRm_NotMounted_ExitsZero verifies that removing a non-existent mount
// (server returns 204 no-op) exits 0.
func TestShareRm_NotMounted_ExitsZero(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"share", "rm", "my-app", "src-app"})
	if err := root.Execute(); err != nil {
		t.Fatalf("share rm not-mounted should exit 0, got: %v", err)
	}
}

// ── env set (unchanged = skip restart) ─────────────────────────────────────

// TestEnvSet_Unchanged_SkipsRestartSideEffect verifies that when the server
// reports {"changed":false}, the CLI does not attempt a restart even with
// --restart, and renders status="unchanged".
func TestEnvSet_Unchanged_SkipsRestart(t *testing.T) {
	resetFormatState(t)
	restartCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/env/") {
			// The query must NOT have restart=true when the CLI skips the restart.
			if r.URL.Query().Get("restart") == "true" {
				restartCalled = true
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"key":"PORT","secret":false,"set":true,"changed":false,"restarted":false}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"env", "set", "my-app", "PORT=8080", "--restart", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("env set unchanged: unexpected error: %v", err)
	}
	// When response is changed:false, the CLI must NOT include restart=true in the query.
	if restartCalled {
		t.Error("CLI should not send restart=true when server reports changed=false")
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout not JSON: %s", out.String())
	}
	if obj["status"] != "unchanged" {
		t.Errorf("status = %v, want unchanged", obj["status"])
	}
}

// TestEnvSet_Changed_IncludesRestartParam verifies that when the value changes,
// the CLI sends restart=true when --restart is given.
func TestEnvSet_Changed_IncludesRestartParam(t *testing.T) {
	resetFormatState(t)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"key":"PORT","secret":false,"set":true,"changed":true,"restarted":true}`)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"env", "set", "my-app", "PORT=9090", "--restart"})
	_ = root.Execute()

	if !strings.Contains(gotQuery, "restart=true") {
		t.Errorf("expected restart=true in query; got %q", gotQuery)
	}
}

// ── schedule add (identical config = no-op) ─────────────────────────────────

// TestScheduleAdd_IdenticalConfig_ExitsZero verifies that schedule add with
// a server 200 no-op (identical config) exits 0 and renders status="unchanged".
func TestScheduleAdd_IdenticalConfig_ExitsZero(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/schedules") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK) // no-op
			fmt.Fprint(w, `{"id":5,"app_id":1,"name":"daily-job","cron_expr":"0 2 * * *","command":["Rscript","daily.R"],"enabled":true,"timeout_seconds":300,"overlap_policy":"skip","missed_policy":"skip","timezone_inherited":true,"effective_timezone":"UTC"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{
		"schedule", "add", "my-app",
		"--name", "daily-job",
		"--cron", "0 2 * * *",
		"--cmd", "Rscript daily.R",
		"-o", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("schedule add identical config: unexpected error: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout not JSON: %s", out.String())
	}
	if obj["status"] != "unchanged" {
		t.Errorf("status = %v, want unchanged", obj["status"])
	}
}

// TestScheduleAdd_DifferentConfig_ExitsConflict verifies that schedule add with
// a server 409 conflict (different config) surfaces as a conflict error (exit 5).
func TestScheduleAdd_DifferentConfig_ExitsConflict(t *testing.T) {
	resetFormatState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/schedules") {
			writeJSONError(w, http.StatusConflict, "schedule with that name already exists for this app")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")

	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"schedule", "add", "my-app",
		"--name", "daily-job",
		"--cron", "0 6 * * *",
		"--cmd", "Rscript daily.R",
	})
	err := root.Execute()
	if err == nil {
		t.Fatal("schedule add with conflict should return an error")
	}
	kind, _ := classify(err)
	if kind != KindConflict {
		t.Errorf("kind = %q, want conflict", kind)
	}
}
