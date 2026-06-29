package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
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

func TestRenderApplyReport_ShowsFailureKindAndPerAttempt(t *testing.T) {
	res := []applyResult{{
		slug: "cb", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
		err: errors.New("deploy cb failed: HTTP 500"),
		attemptsDetail: []attemptOutcome{
			{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"},
			{Attempt: 2, Kind: deployfail.Crashed, Err: "y"},
		},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", res, false)
	out := buf.String()
	if !strings.Contains(out, "failed [crashed]") {
		t.Fatalf("failed line must show the final kind, got:\n%s", out)
	}
	if !strings.Contains(out, "attempt 1: readiness_timeout") || !strings.Contains(out, "attempt 2: crashed") {
		t.Fatalf("must list each failed attempt's kind, got:\n%s", out)
	}
}

func TestRenderApplyReport_RetriedSuccessShowsEarlierFailure(t *testing.T) {
	res := []applyResult{{
		slug: "flaky", action: fleet.ActionUpdateSource, status: statusUpdated, attempts: 2,
		attemptsDetail: []attemptOutcome{{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"}},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", res, false)
	out := buf.String()
	if !strings.Contains(out, "attempt 1: readiness_timeout") {
		t.Fatalf("a retried success must still surface attempt 1's reason, got:\n%s", out)
	}
	if strings.Contains(out, "updated [") {
		t.Fatalf("a successful status must not get a [kind] tag, got:\n%s", out)
	}
}

// JSON assertions unmarshal the envelope (the test is package cli, so it can
// read the unexported jsonResult). A string search cannot tell a TOP-LEVEL
// failure_kind from the failure_kind keys nested in attempt_details, so it would
// reject correct output; assert on the decoded struct instead.
func TestWriteFleetApplyJSON_FailureKindAndAttemptDetails(t *testing.T) {
	d := fleet.AppDiff{Slug: "cb", Action: fleet.ActionUpdateSource}
	r := applyResult{
		slug: "cb", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
		err: errors.New("boom"),
		attemptsDetail: []attemptOutcome{
			{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"},
			{Attempt: 2, Kind: deployfail.Crashed, Err: "y"},
		},
	}
	var buf bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, "http://h", []fleet.AppDiff{d}, []applyResult{r}, 4, "PARTIAL"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	got := env.Apps[0].Result
	if got == nil {
		t.Fatal("result missing from JSON envelope")
	}
	if got.FailureKind != "crashed" {
		t.Fatalf("top-level failure_kind = %q, want crashed", got.FailureKind)
	}
	if len(got.AttemptDetails) != 2 ||
		got.AttemptDetails[0].FailureKind != "readiness_timeout" ||
		got.AttemptDetails[1].FailureKind != "crashed" {
		t.Fatalf("attempt_details wrong: %+v", got.AttemptDetails)
	}
}

func TestWriteFleetApplyJSON_RetriedSuccessHasDetailsButNoTopLevelKind(t *testing.T) {
	d := fleet.AppDiff{Slug: "flaky", Action: fleet.ActionUpdateSource}
	r := applyResult{
		slug: "flaky", action: fleet.ActionUpdateSource, status: statusUpdated, attempts: 2,
		attemptsDetail: []attemptOutcome{{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"}},
	}
	var buf bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, "http://h", []fleet.AppDiff{d}, []applyResult{r}, 0, "OK"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	got := env.Apps[0].Result
	if got == nil || len(got.AttemptDetails) != 1 {
		t.Fatalf("retried success must keep exactly one attempt_detail, got %+v", got)
	}
	if got.FailureKind != "" {
		t.Fatalf("a non-failed result must omit the top-level failure_kind, got %q", got.FailureKind)
	}
}
