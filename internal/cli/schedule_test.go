package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSchedule_Add_ShellwordsCmd verifies that --cmd parses the shell string
// into a JSON array in the request body.
func TestSchedule_Add_ShellwordsCmd(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":1,"name":"fetch","cron_expr":"0 * * * *","command":["python","helpers/fetch.py","--flag","x"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip"}`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"add", "demo", "--name", "fetch", "--cron", "0 * * * *", "--cmd", "python helpers/fetch.py --flag x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/schedules" {
		t.Errorf("unexpected path: %s", req.Path)
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	cmdRaw, ok := body["command"]
	if !ok {
		t.Fatal("expected 'command' in body")
	}
	cmdSlice, ok := cmdRaw.([]any)
	if !ok {
		t.Fatalf("expected command to be array, got %T", cmdRaw)
	}
	want := []string{"python", "helpers/fetch.py", "--flag", "x"}
	if len(cmdSlice) != len(want) {
		t.Fatalf("expected command len %d, got %d: %v", len(want), len(cmdSlice), cmdSlice)
	}
	for i, w := range want {
		if cmdSlice[i] != w {
			t.Errorf("command[%d]: expected %q, got %q", i, w, cmdSlice[i])
		}
	}
}

// TestSchedule_Add_PrintsDSTAdvisory verifies that when the server returns a
// dst_advisory on the created schedule, the CLI surfaces it to stderr so the
// developer learns their fixed-hour job will fire twice on the fall-back day.
func TestSchedule_Add_PrintsDSTAdvisory(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"id":7,"name":"nightly","cron_expr":"30 2 * * *","command":["echo","hi"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip","effective_timezone":"Europe/Amsterdam","dst_advisory":"Schedule fires twice on 2025-10-26: Europe/Amsterdam observes daylight saving time and this wall-clock time recurs when clocks fall back. Use UTC or a time outside the transition hour to fire once."}`)

	cmd := newScheduleCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"add", "demo", "--name", "nightly", "--cron", "30 2 * * *", "--cmd", "echo hi", "--timezone", "Europe/Amsterdam"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stderr.String(), "fires twice on 2025-10-26") {
		t.Errorf("expected DST advisory on stderr, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestSchedule_Add_NoDSTAdvisoryWhenAbsent verifies the CLI prints no advisory
// when the server omits dst_advisory.
func TestSchedule_Add_NoDSTAdvisoryWhenAbsent(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"id":8,"name":"safe","cron_expr":"30 14 * * *","command":["echo","hi"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip","effective_timezone":"Europe/Amsterdam"}`)

	cmd := newScheduleCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"add", "demo", "--name", "safe", "--cron", "30 14 * * *", "--cmd", "echo hi", "--timezone", "Europe/Amsterdam"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stderr.String(), "fires twice") {
		t.Errorf("did not expect a DST advisory, got stderr=%q", stderr.String())
	}
}

// TestSchedule_Add_CmdJSON verifies that --cmd-json parses a JSON array directly.
func TestSchedule_Add_CmdJSON(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":2,"name":"run","cron_expr":"0 * * * *","command":["python","x.py"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip"}`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"add", "demo", "--name", "run", "--cron", "0 * * * *", "--cmd-json", `["python","x.py"]`})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	cmdSlice, ok := body["command"].([]any)
	if !ok {
		t.Fatalf("expected command to be array")
	}
	want := []string{"python", "x.py"}
	if len(cmdSlice) != len(want) {
		t.Fatalf("expected command len %d, got %d", len(want), len(cmdSlice))
	}
	for i, w := range want {
		if cmdSlice[i] != w {
			t.Errorf("command[%d]: expected %q, got %q", i, w, cmdSlice[i])
		}
	}
}

// TestSchedule_Add_RequiresNameAndCron verifies that omitting --name or --cron
// causes a cobra "required flag" error without issuing any HTTP request.
func TestSchedule_Add_RequiresNameAndCron(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	// Missing both --name and --cron
	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"add", "demo", "--cmd", "python x.py"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error for missing required flags, got nil")
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests, got %d", len(*reqs))
	}
}

// TestSchedule_Ls_FormatsRows verifies that ls prints both schedule names.
func TestSchedule_Ls_FormatsRows(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[
		{"id":1,"name":"daily","cron_expr":"0 0 * * *","command":["Rscript","run.R"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip"},
		{"id":2,"name":"hourly","cron_expr":"0 * * * *","command":["python","go.py"],"enabled":false,"timeout_seconds":600,"overlap_policy":"queue","missed_policy":"run_once"}
	]`)

	cmd := newScheduleCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"ls", "demo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "daily") {
		t.Errorf("expected output to contain 'daily', got:\n%s", out)
	}
	if !strings.Contains(out, "hourly") {
		t.Errorf("expected output to contain 'hourly', got:\n%s", out)
	}
}

// TestSchedule_Rm_ResolvesNameToID verifies that rm does a GET to list schedules,
// then issues a DELETE to the correct numeric ID path.
func TestSchedule_Rm_ResolvesNameToID(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	// Both list and delete requests go to the same test server.
	// We set the response to the list body; the DELETE also gets a 200, which is
	// fine — we only care that the correct path was targeted.
	setResp(200, `[{"id":42,"name":"hello","cron_expr":"0 * * * *","command":["echo","hi"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"rm", "demo", "hello"})
	_ = cmd.Execute() // DELETE may see 200 instead of 204 — that's acceptable here

	if len(*reqs) < 2 {
		t.Fatalf("expected at least 2 requests (list + delete), got %d", len(*reqs))
	}
	listReq := (*reqs)[0]
	if listReq.Method != "GET" {
		t.Errorf("expected first request to be GET, got %s", listReq.Method)
	}
	if listReq.Path != "/api/apps/demo/schedules" {
		t.Errorf("unexpected list path: %s", listReq.Path)
	}
	deleteReq := (*reqs)[1]
	if deleteReq.Method != "DELETE" {
		t.Errorf("expected second request to be DELETE, got %s", deleteReq.Method)
	}
	if deleteReq.Path != "/api/apps/demo/schedules/42" {
		t.Errorf("expected DELETE /api/apps/demo/schedules/42, got %s", deleteReq.Path)
	}
}

// TestSchedule_Add_IfNotExists_409ExitsZero verifies that when the server returns
// 409 Conflict and --if-not-exists is set, the CLI exits 0 with no output.
func TestSchedule_Add_IfNotExists_409ExitsZero(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"schedule with that name already exists for this app"}`)

	var outBuf, errBuf bytes.Buffer
	cmd := newScheduleCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"add", "demo", "--name", "fetch", "--cron", "0 * * * *", "--cmd", "python run.py", "--if-not-exists"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected nil error with --if-not-exists on 409, got: %v", err)
	}
	if out := outBuf.String(); out != "" {
		t.Errorf("expected no stdout with --if-not-exists on 409, got: %q", out)
	}
	if errOut := errBuf.String(); errOut != "" {
		t.Errorf("expected no stderr with --if-not-exists on 409, got: %q", errOut)
	}
}

// TestSchedule_Add_NoIfNotExists_409Errors verifies that without --if-not-exists
// a 409 response from the server surfaces as an error (existing behaviour).
func TestSchedule_Add_NoIfNotExists_409Errors(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(409, `{"error":"schedule with that name already exists for this app"}`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"add", "demo", "--name", "fetch", "--cron", "0 * * * *", "--cmd", "python run.py"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error without --if-not-exists on 409, got nil")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Errorf("error should mention status code 409, got: %v", err)
	}
}

// TestScheduleCmd_RegisteredWithRoot verifies schedule is registered with the root command.
func TestScheduleCmd_RegisteredWithRoot(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	AddCommandsTo(root)
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Use == "schedule" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'schedule' command to be registered with root")
	}
}

// TestSchedule_Logs_HasFollowFlag verifies the logs subcommand exposes --follow.
func TestSchedule_Logs_HasFollowFlag(t *testing.T) {
	cmd := newScheduleCmd()
	logs, _, err := cmd.Find([]string{"logs"})
	if err != nil {
		t.Fatalf("find logs: %v", err)
	}
	if logs.Flags().Lookup("follow") == nil {
		t.Error("expected logs subcommand to expose --follow flag")
	}
	if logs.Flags().Lookup("run") == nil {
		t.Error("expected logs subcommand to expose --run flag")
	}
}

// scheduleTestServer wires a minimal multi-route server for the schedule
// follow/exit-code flows and writes a CLI config pointing at it. routes maps
// "METHOD /path" (path only, no query) to a handler.
func scheduleTestServer(t *testing.T, routes map[string]http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if h, ok := routes[key]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected request: %s", key)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "shinyhub")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// EXIT-3: `schedule logs --follow` consumes an SSE stream; the CLI must strip
// the "data: " framing and drop heartbeat/blank lines so the user sees the raw
// log content, not event-stream syntax.
func TestScheduleLogs_FollowStripsSSEFraming(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job"}]`)
		},
		"GET /api/apps/demo/schedules/7/runs/3/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: hello\n\n: heartbeat\n\ndata: world\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/3": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"succeeded","exit_code":0}`)
		},
	})

	out, err := execCLI(t, "schedule", "logs", "demo", "job", "--follow", "--run", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "data:") {
		t.Errorf("output must not contain SSE framing:\n%s", out)
	}
	if strings.Contains(out, "heartbeat") {
		t.Errorf("output must not contain heartbeat comments:\n%s", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("output missing log content:\n%s", out)
	}
}

// EXIT-2: `schedule run --follow` must exit non-zero when the run finishes in a
// failure state. The exit code mirrors the scheduled command's own exit code.
func TestScheduleRun_FollowExitsNonZeroOnFailure(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job"}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"run_id":9}`)
		},
		"GET /api/apps/demo/schedules/7/runs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":9}]`)
		},
		"GET /api/apps/demo/schedules/7/runs/9/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: boom\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/9": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"failed","exit_code":2}`)
		},
	})

	_, err := execCLI(t, "schedule", "run", "demo", "job", "--follow")
	if err == nil {
		t.Fatal("expected non-nil error for a failed run")
	}
	if exitCode(err) != 2 {
		t.Errorf("exit code = %d, want 2 (the run's own exit code)", exitCode(err))
	}
}

// SCH-2: `schedule run --follow` must follow the exact run it just triggered,
// using the run_id returned by POST /run rather than re-querying the latest
// run (which races a concurrent cron tick). The server here exposes no
// runs?limit=1 route, so any fallback to that endpoint fails the test.
func TestScheduleRun_FollowUsesReturnedRunID(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job"}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"started","run_id":42}`)
		},
		"GET /api/apps/demo/schedules/7/runs/42/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: ok\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/42": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"succeeded","exit_code":0}`)
		},
	})

	if _, err := execCLI(t, "schedule", "run", "demo", "job", "--follow"); err != nil {
		t.Fatalf("expected clean exit using the returned run_id, got: %v", err)
	}
}

// A run that finishes successfully must exit 0.
func TestScheduleRun_FollowExitsZeroOnSuccess(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job"}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"run_id":9}`)
		},
		"GET /api/apps/demo/schedules/7/runs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":9}]`)
		},
		"GET /api/apps/demo/schedules/7/runs/9/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: ok\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/9": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"succeeded","exit_code":0}`)
		},
	})

	if _, err := execCLI(t, "schedule", "run", "demo", "job", "--follow"); err != nil {
		t.Fatalf("expected clean exit for a succeeded run, got: %v", err)
	}
}

// SCH-5: triggering a DISABLED schedule still runs it (a deliberate manual
// override), but the CLI must say so on stderr - otherwise an operator who
// disabled the schedule sees a plain success line and assumes it was enabled.
func TestScheduleRun_DisabledSchedulePrintsNote(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job","enabled":false}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"run_id":9}`)
		},
	})

	stdout, stderr, err := execCLISplit(t, "schedule", "run", "demo", "job")
	if err != nil {
		t.Fatalf("manual trigger of a disabled schedule should still succeed, got: %v", err)
	}
	if !strings.Contains(stderr, "disabled") {
		t.Errorf("expected a disabled-schedule note on stderr, got stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, "started") {
		t.Errorf("expected the normal started line on stdout, got %q", stdout)
	}
}

// An enabled schedule must NOT print the disabled note.
func TestScheduleRun_EnabledScheduleNoNote(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job","enabled":true}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"run_id":9}`)
		},
	})

	_, stderr, err := execCLISplit(t, "schedule", "run", "demo", "job")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stderr, "disabled") {
		t.Errorf("enabled schedule must not print a disabled note, got stderr=%q", stderr)
	}
}

// CR2-1: when a disabled schedule's manual trigger FAILS, the CLI must not claim
// the trigger "proceeded anyway" - that note may only appear once the server has
// actually accepted the run.
func TestScheduleRun_DisabledScheduleFailureNoProceededNote(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job","enabled":false}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"boom"}`)
		},
	})

	_, stderr, err := execCLISplit(t, "schedule", "run", "demo", "job")
	if err == nil {
		t.Fatal("a failed manual trigger must return an error")
	}
	if strings.Contains(stderr, "proceeded") {
		t.Errorf("must not claim the trigger proceeded when it failed, got stderr=%q", stderr)
	}
}

// EXIT-4: runFinalExitError must set Kind=KindJobFailed so the error envelope
// carries kind=job_failed, the process exit mirrors the job's own code, and the
// envelope omits exit_code (passthrough contract). The test drives the full
// --follow code path with a server that returns exit_code=7 and status=failed,
// then pipes the returned error through reportTo to assert the envelope shape.
func TestScheduleRun_FollowJobFailedEnvelopeOmitsExitCode(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"GET /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"id":7,"name":"job","enabled":true}]`)
		},
		"POST /api/apps/demo/schedules/7/run": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"run_id":11}`)
		},
		"GET /api/apps/demo/schedules/7/runs/11/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: error output\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/11": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"failed","exit_code":7}`)
		},
	})

	_, _, runErr := execCLISplit(t, "schedule", "run", "demo", "job", "--follow")
	if runErr == nil {
		t.Fatal("expected non-nil error for a failed run")
	}
	if exitCode(runErr) != 7 {
		t.Errorf("exit code = %d, want 7 (job's own exit code)", exitCode(runErr))
	}

	// Verify the error classifies as KindJobFailed so Report() emits the right
	// envelope. Drive reportTo directly: it is the same code path main() calls.
	var stderrBuf bytes.Buffer
	code := reportTo(&stderrBuf, false, formatJSON, runErr)
	if code != 7 {
		t.Errorf("reportTo exit code = %d, want 7", code)
	}
	line := strings.TrimRight(stderrBuf.String(), "\n")
	var env map[string]any
	if jerr := json.Unmarshal([]byte(line), &env); jerr != nil {
		t.Fatalf("reportTo output is not JSON: %v\nraw: %q", jerr, line)
	}
	errObj, _ := env["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("envelope missing error object: %v", env)
	}
	if errObj["kind"] != "job_failed" {
		t.Errorf("envelope kind = %q, want job_failed", errObj["kind"])
	}
	if _, has := errObj["exit_code"]; has {
		t.Errorf("job_failed envelope must omit exit_code (passthrough), got %v", errObj["exit_code"])
	}
}

// SCH-1: `schedule update` changes a schedule in place (preserving run history)
// by PATCHing only the fields the operator actually supplied. A lone --cron must
// not clobber the command, timeout, or any other stored field.
func TestSchedule_Update_SendsOnlyChangedFields(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job","cron_expr":"0 * * * *","command":["python","x.py"],"enabled":true,"timeout_seconds":3600,"overlap_policy":"skip","missed_policy":"skip"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--cron", "30 2 * * *"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests (list + patch), got %d", len(*reqs))
	}
	patch := (*reqs)[1]
	if patch.Method != "PATCH" {
		t.Errorf("expected PATCH, got %s", patch.Method)
	}
	if patch.Path != "/api/apps/demo/schedules/7" {
		t.Errorf("unexpected patch path: %s", patch.Path)
	}

	var body map[string]any
	if err := json.Unmarshal(patch.Body, &body); err != nil {
		t.Fatalf("unmarshal patch body: %v", err)
	}
	if body["cron_expr"] != "30 2 * * *" {
		t.Errorf("expected cron_expr in body, got %v", body["cron_expr"])
	}
	for _, k := range []string{"name", "command", "timeout_seconds", "overlap_policy", "missed_policy", "enabled", "timezone"} {
		if _, ok := body[k]; ok {
			t.Errorf("unsupplied field %q must be absent from PATCH body, got %v", k, body[k])
		}
	}
}

// SCH-1 timezone tri-state: --clear-timezone must send an explicit JSON null so
// the server clears the per-schedule zone back to inherit-server-default.
func TestSchedule_Update_ClearTimezoneSendsNull(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job","cron_expr":"0 * * * *","command":["python","x.py"],"enabled":true,"timezone":"Europe/Amsterdam"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--clear-timezone"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	patch := (*reqs)[1]
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(patch.Body, &raw); err != nil {
		t.Fatalf("unmarshal patch body: %v", err)
	}
	tz, ok := raw["timezone"]
	if !ok {
		t.Fatal("expected timezone key present in PATCH body for --clear-timezone")
	}
	if string(tz) != "null" {
		t.Errorf("expected timezone null, got %s", tz)
	}
}

// SCH-1 timezone tri-state: --timezone <zone> sets the per-schedule zone.
func TestSchedule_Update_SetTimezone(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job","cron_expr":"0 * * * *","command":["python","x.py"],"enabled":true}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--timezone", "Europe/Amsterdam"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[1].Body, &body); err != nil {
		t.Fatalf("unmarshal patch body: %v", err)
	}
	if body["timezone"] != "Europe/Amsterdam" {
		t.Errorf("expected timezone Europe/Amsterdam, got %v", body["timezone"])
	}
}

// CR2-11: the --cmd / --cmd-json mutual exclusion must be detected by flag
// presence, not value. An explicitly-empty --cmd alongside a valid --cmd-json
// must be rejected rather than letting the value-based check miss the conflict
// and PATCH an empty command.
func TestSchedule_Update_EmptyCmdWithCmdJSONConflicts(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--cmd", "", "--cmd-json", `["python","run.py"]`})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for --cmd together with --cmd-json, even when --cmd is empty")
	}
	for _, r := range *reqs {
		if r.Method == "PATCH" {
			t.Errorf("no PATCH should be issued on conflicting command flags")
		}
	}
}

// SCH-1: --timezone and --clear-timezone are mutually exclusive.
func TestSchedule_Update_TimezoneAndClearConflict(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--timezone", "UTC", "--clear-timezone"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for --timezone with --clear-timezone")
	}
	for _, r := range *reqs {
		if r.Method == "PATCH" {
			t.Errorf("no PATCH should be issued on conflicting timezone flags")
		}
	}
}

// SCH-1: update with no field flags is a no-op mistake; error without touching
// the server (beyond resolving the name) rather than sending an empty PATCH.
func TestSchedule_Update_NoFieldsErrors(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job"}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no update flags are supplied")
	}
	for _, r := range *reqs {
		if r.Method == "PATCH" {
			t.Errorf("no PATCH should be issued when nothing changed")
		}
	}
}

// SCH-1: --cmd parses a shell string into the command array.
func TestSchedule_Update_CmdShellwords(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"id":7,"name":"job","command":["old"]}]`)

	cmd := newScheduleCmd()
	cmd.SetArgs([]string{"update", "demo", "job", "--cmd", "python new.py --flag x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*reqs)[1].Body, &body); err != nil {
		t.Fatalf("unmarshal patch body: %v", err)
	}
	cmdSlice, ok := body["command"].([]any)
	if !ok {
		t.Fatalf("expected command array in body, got %T", body["command"])
	}
	want := []string{"python", "new.py", "--flag", "x"}
	if len(cmdSlice) != len(want) {
		t.Fatalf("command len = %d, want %d: %v", len(cmdSlice), len(want), cmdSlice)
	}
	for i, w := range want {
		if cmdSlice[i] != w {
			t.Errorf("command[%d] = %q, want %q", i, cmdSlice[i], w)
		}
	}
}

// TestScheduleAdd_RunOnRegister_ReportsFirstFire verifies that --run-on-register
// sends run_on_register:true in the body and prints the first-fire run id when
// the server returns one.
func TestScheduleAdd_RunOnRegister_ReportsFirstFire(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":7,"name":"warm","first_fire_run_id":42}`)

	cmd := newScheduleAddCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"warmapp", "--name", "warm", "--cron", "0 5 * * *", "--cmd", "true", "--run-on-register"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var gotBody map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &gotBody); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if gotBody["run_on_register"] != true {
		t.Errorf("run_on_register in body = %v, want true", gotBody["run_on_register"])
	}
	// In piped mode the output is a JSON envelope; check for the run id value.
	if !strings.Contains(out.String(), "42") {
		t.Errorf("output missing first-fire run id; got: %s", out.String())
	}
}

// TestScheduleAdd_NoFirstFire_NoTriggerLine verifies that when the server's
// create response omits first_fire_run_id (schedule already succeeded, or
// --run-on-register was not passed), the CLI prints the normal "created
// schedule" line, does not print any "first-fire" line, and exits 0. Also
// confirms --follow alone (without a first-fire run id) is silently ignored.
func TestScheduleAdd_NoFirstFire_NoTriggerLine(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"id":7,"name":"warm"}`)

	cmd := newScheduleAddCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// --follow passed without --run-on-register must be a no-op.
	cmd.SetArgs([]string{"warmapp", "--name", "warm", "--cron", "0 5 * * *", "--cmd", "true", "--follow"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out.String(), "first-fire") {
		t.Errorf("unexpected first-fire line when server returned no run id; got: %s", out.String())
	}
	// In piped mode the output is a JSON envelope with status "created".
	if !strings.Contains(out.String(), `"created"`) {
		t.Errorf("missing created status in output; got: %s", out.String())
	}
}

// FORMAT-5: schedule add --run-on-register --follow must stream NDJSON log
// objects on stdout when piped, and route the creation notice to stderr.
// This verifies that the streaming format (resolved once for the follow path)
// is not corrupted by the creation envelope logic.
func TestScheduleAdd_RunOnRegister_Follow_NdjsonStream(t *testing.T) {
	scheduleTestServer(t, map[string]http.HandlerFunc{
		"POST /api/apps/demo/schedules": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":7,"name":"warm","first_fire_run_id":42}`)
		},
		"GET /api/apps/demo/schedules/7/runs/42/logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: warming up\n\ndata: done\n\n")
		},
		"GET /api/apps/demo/schedules/7/runs/42": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"succeeded","exit_code":0}`)
		},
	})

	stdout, stderr, err := execCLISplit(t, "schedule", "add", "demo",
		"--name", "warm", "--cron", "0 5 * * *", "--cmd", "true",
		"--run-on-register", "--follow")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	// The creation notice must appear on stderr, not stdout.
	if !strings.Contains(stderr, "created") && !strings.Contains(stderr, "following") {
		t.Errorf("creation notice missing from stderr; stderr=%q stdout=%q", stderr, stdout)
	}

	// Stdout must be NDJSON log objects, not an action envelope.
	// Each log line is wrapped as {"line":"..."} in NDJSON mode.
	if strings.Contains(stdout, `"status"`) {
		t.Errorf("stdout must not contain action envelope in follow mode; stdout=%q", stdout)
	}
	// The log content must appear on stdout wrapped in NDJSON.
	if !strings.Contains(stdout, `"line"`) {
		t.Errorf("stdout missing NDJSON log objects; stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "warming up") {
		t.Errorf("stdout missing log content 'warming up'; stdout=%q", stdout)
	}
}

// TestShareCmd_RegisteredWithRoot verifies share is registered with the root command.
func TestShareCmd_RegisteredWithRoot(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	AddCommandsTo(root)
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Use == "share" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'share' command to be registered with root")
	}
}
