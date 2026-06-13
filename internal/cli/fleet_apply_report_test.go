package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestApplyExitCode_HighestOfFourFiveEnumeratesBoth(t *testing.T) {
	res := []applyResult{
		{slug: "a", status: statusFailed},
		{slug: "b", status: statusConflict},
		{slug: "c", status: statusCreated},
	}
	code, reason := applyExitCode(res)
	if code != 5 {
		t.Fatalf("code = %d, want 5 (conflict outranks failure)", code)
	}
	if !strings.Contains(reason, "failed") || !strings.Contains(reason, "conflict") {
		t.Fatalf("reason must enumerate both: %q", reason)
	}
}

func TestApplyExitCode_FailuresOnly(t *testing.T) {
	code, reason := applyExitCode([]applyResult{{status: statusFailed}})
	if code != 4 || !strings.Contains(reason, "failed") {
		t.Fatalf("code=%d reason=%q, want 4/failed", code, reason)
	}
}

func TestApplyExitCode_AllGood(t *testing.T) {
	code, reason := applyExitCode([]applyResult{{status: statusUnchanged}, {status: statusCreated}})
	if code != 0 || !strings.Contains(strings.ToUpper(reason), "OK") {
		t.Fatalf("code=%d reason=%q, want 0/OK", code, reason)
	}
}

func TestRenderApplyReport_TableSummaryAndNextCommand(t *testing.T) {
	var out bytes.Buffer
	res := []applyResult{
		{slug: "sales", action: fleet.ActionCreate, status: statusCreated, attempts: 1, duration: 12300 * time.Millisecond},
		{slug: "weekly", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
			duration: 2 * time.Second, err: errStub("health check timeout")},
	}
	err := renderApplyReport(&out, "prod-eu", res, false)
	if err == nil || exitCode(err) != 4 {
		t.Fatalf("want exit 4 error, got %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Applied:") || !strings.Contains(s, "Result:") {
		t.Fatalf("missing summary/result lines:\n%s", s)
	}
	if !strings.Contains(s, "shinyhub apps logs weekly --tail 200") {
		t.Fatalf("failure must end with the apps-logs next-command:\n%s", s)
	}
	if strings.Contains(s, "shinyhub logs weekly") {
		t.Fatalf("must not point at the non-existent top-level 'shinyhub logs':\n%s", s)
	}
}

func TestRenderApplyReport_QuietCollapses(t *testing.T) {
	var out bytes.Buffer
	_ = renderApplyReport(&out, "eu", []applyResult{{slug: "a", status: statusUnchanged}}, true)
	s := out.String()
	if strings.Contains(s, "fleet_id=") {
		t.Fatalf("quiet must omit the header/table: %q", s)
	}
	if !strings.Contains(s, "Result:") {
		t.Fatalf("quiet must keep the result line: %q", s)
	}
}

func TestWriteFleetApplyJSON_HasResultAndSummary(t *testing.T) {
	var out bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	diff := []fleet.AppDiff{{Slug: "a", Action: fleet.ActionCreate}}
	res := []applyResult{{slug: "a", action: fleet.ActionCreate, status: statusCreated, attempts: 1, duration: time.Second}}
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, res, 0, "OK - all converged"); err != nil {
		t.Fatalf("json: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	apps := env["apps"].([]any)
	a0 := apps[0].(map[string]any)
	if a0["result"] == nil {
		t.Fatalf("per-app result missing: %v", a0)
	}
	sum := env["summary"].(map[string]any)
	if sum["exit_code"].(float64) != 0 || sum["exit_reason"] == "" {
		t.Fatalf("summary missing exit fields: %v", sum)
	}
}

// TestWriteFleetApplyJSON_IncludesAppURL verifies each applied app carries its
// served URL, so CI can post a link to the app from a PR without a follow-up
// `apps list`.
func TestWriteFleetApplyJSON_IncludesAppURL(t *testing.T) {
	var out bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	diff := []fleet.AppDiff{{Slug: "reports", Action: fleet.ActionCreate}}
	res := []applyResult{{slug: "reports", action: fleet.ActionCreate, status: statusCreated, attempts: 1, duration: time.Second}}
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, res, 0, "OK - all converged"); err != nil {
		t.Fatalf("json: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	a0 := env["apps"].([]any)[0].(map[string]any)
	if got := a0["app_url"]; got != "https://h/app/reports/" {
		t.Errorf("app_url = %v, want https://h/app/reports/", got)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
