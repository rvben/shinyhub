package cli

import (
	"bytes"
	"encoding/json"
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
