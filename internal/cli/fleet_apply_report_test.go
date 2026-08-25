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

func TestApplyExitCode_SkippedDoesNotClaimConvergence(t *testing.T) {
	res := []applyResult{{slug: "legacy", status: statusSkipped, note: "re-run with --adopt"}}
	code, reason := applyExitCode(res)
	if code != 0 {
		t.Fatalf("code = %d, want the intentional-skip compatibility code 0", code)
	}
	if strings.Contains(reason, "all converged") || !strings.Contains(reason, "skipped") {
		t.Fatalf("reason = %q, must describe the skip without claiming convergence", reason)
	}
	if got := applyRunStatus(code, tallyResults(res)); got != "skipped" {
		t.Fatalf("run status = %q, want skipped", got)
	}
	if got := applyRecoveryFor(applyReportContext{}, res).Strategy; got != "review_skipped" {
		t.Fatalf("recovery strategy = %q, want review_skipped", got)
	}
}

func TestApplyOutcomeExitFailsWhenRunCompletionIsUnrecorded(t *testing.T) {
	o := applyOutcome{
		apps:     []applyResult{{slug: "ok", status: statusUnchanged}},
		runError: errors.New("connection reset"),
	}
	code, reason := applyOutcomeExit(o)
	if code != 4 || !strings.Contains(reason, "completion was not recorded") {
		t.Fatalf("exit = %d %q, want partial run-recording failure", code, reason)
	}
	var out bytes.Buffer
	_ = renderApplyReport(&out, "prod", o, false)
	if !strings.Contains(out.String(), "fleet run completion was not recorded") || !strings.Contains(out.String(), "connection reset") {
		t.Fatalf("report = %q", out.String())
	}
}

func TestRenderApplyReport_TableSummaryAndNextCommand(t *testing.T) {
	var out bytes.Buffer
	res := []applyResult{
		{slug: "sales", action: fleet.ActionCreate, status: statusCreated, attempts: 1, duration: 12300 * time.Millisecond},
		{slug: "weekly", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
			duration: 2 * time.Second, err: errStub("health check timeout")},
	}
	err := renderApplyReport(&out, "prod-eu", applyOutcome{apps: res}, false)
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

// TestRenderApplyReport_SlugColumnFitsTheLongestSlug pins the alignment a fixed
// 24-character slug column silently broke: the status word must start at the
// same offset on every row, including the row whose slug is longer than any
// width that could have been picked in advance.
func TestRenderApplyReport_SlugColumnFitsTheLongestSlug(t *testing.T) {
	var out bytes.Buffer
	res := []applyResult{
		{slug: "sales", status: statusCreated},
		{slug: "quarterly-revenue-reporting-dashboard", status: statusUnchanged},
	}
	_ = renderApplyReport(&out, "eu", applyOutcome{apps: res}, false)

	var short, long string
	for _, ln := range strings.Split(out.String(), "\n") {
		switch {
		case strings.Contains(ln, "sales"):
			short = ln
		case strings.Contains(ln, "quarterly-revenue-reporting-dashboard"):
			long = ln
		}
	}
	if short == "" || long == "" {
		t.Fatalf("both app rows must be printed:\n%s", out.String())
	}
	if got, want := strings.Index(short, "created"), strings.Index(long, "unchanged"); got != want {
		t.Errorf("status column starts at %d for the short slug and %d for the long one:\n%s",
			got, want, out.String())
	}
}

func TestRenderApplyReport_QuietCollapses(t *testing.T) {
	var out bytes.Buffer
	_ = renderApplyReport(&out, "eu", applyOutcome{apps: []applyResult{{slug: "a", status: statusUnchanged}}}, true)
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
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, nil, applyOutcome{apps: res}, 0, "OK - all converged"); err != nil {
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

func TestWriteFleetApplyJSON_CarriesReasonAndWarnings(t *testing.T) {
	var out bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	diff := []fleet.AppDiff{{Slug: "legacy", Action: fleet.ActionAdopt}}
	res := []applyResult{{
		slug: "legacy", action: fleet.ActionAdopt, status: statusSkipped,
		note:     "present, not owned; re-run with --adopt",
		warnings: []string{"post-deploy hooks were skipped"},
	}}
	code, reason := applyExitCode(res)
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, nil, applyOutcome{apps: res}, code, reason); err != nil {
		t.Fatal(err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Status != "skipped" || env.Apps[0].Result.Reason == "" || len(env.Apps[0].Result.Warnings) != 1 {
		t.Fatalf("structured skipped result = %+v", env)
	}
}

func TestWriteFleetApplyJSON_IncludesSharedBundleInputs(t *testing.T) {
	var out bytes.Buffer
	m := &fleet.Manifest{
		FleetID: "eu",
		BundleFiles: []fleet.BundleFileEntry{{
			From: "_shared/theme.py", To: "helpers/theme.py", Consumers: []string{"sales", "ops"},
		}},
	}
	diff := []fleet.AppDiff{
		{Slug: "sales", Action: fleet.ActionUpdateSource},
		{Slug: "ops", Action: fleet.ActionUpdateConfig},
	}
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, nil, applyOutcome{}, 0, "OK - all converged"); err != nil {
		t.Fatalf("json: %v", err)
	}
	var env struct {
		SchemaVersion int              `json:"schema_version"`
		BundleFiles   []jsonBundleFile `json:"bundle_files"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if env.SchemaVersion != 3 {
		t.Fatalf("schema_version = %d, want 3", env.SchemaVersion)
	}
	if len(env.BundleFiles) != 1 {
		t.Fatalf("bundle_files = %+v, want one declaration", env.BundleFiles)
	}
	got := env.BundleFiles[0]
	if got.From != "_shared/theme.py" || got.To != "helpers/theme.py" {
		t.Fatalf("bundle file mapping = %+v", got)
	}
	if strings.Join(got.Consumers, ",") != "sales,ops" {
		t.Fatalf("consumers = %v, want sales,ops", got.Consumers)
	}
	if strings.Join(got.PlannedConsumers, ",") != "sales" {
		t.Fatalf("planned_consumers = %v, want sales", got.PlannedConsumers)
	}
}

func TestApplyReport_PartialRunNamesCommittedWorkAndRecovery(t *testing.T) {
	ctx := applyReportContext{
		RunID: "run-0123456789", FleetID: "eu",
		PlanCommand: "shinyhub fleet plan -f fleet.toml", ResumeCommand: "shinyhub fleet apply -f fleet.toml",
	}
	o := applyOutcome{apps: []applyResult{
		{slug: "done", action: fleet.ActionUpdateSource, status: statusUpdated, mutation: mutationCommitted},
		{slug: "half", action: fleet.ActionUpdateSourceConfig, status: statusFailed, mutation: mutationPartial, err: errors.New("config patch failed")},
		{slug: "raced", action: fleet.ActionUpdateConfig, status: statusConflict, mutation: mutationNone, err: errors.New("revision changed")},
	}}
	var out bytes.Buffer
	err := renderApplyReportWithContext(&out, ctx, o, false)
	if exitCode(err) != 5 {
		t.Fatalf("exit = %d, want conflict exit 5", exitCode(err))
	}
	text := out.String()
	for _, want := range []string{
		"run_id=run-0123456789", "mutation partial", "Remote mutation: 1 committed, 1 partial, 0 unknown",
		"Remote state changed", "shinyhub fleet plan -f fleet.toml", "shinyhub fleet apply -f fleet.toml",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("partial report missing %q:\n%s", want, text)
		}
	}
}

func TestApplyReport_PartialGolden(t *testing.T) {
	ctx := applyReportContext{
		RunID: "9d2f6d1895964dcabf1ba57d5301d9c4", FleetID: "eu",
		PlanCommand:   "shinyhub fleet plan -f fleets/eu.toml",
		ResumeCommand: "shinyhub fleet apply --prune -f fleets/eu.toml",
	}
	o := applyOutcome{apps: []applyResult{
		{slug: "sales", action: fleet.ActionUpdateSource, status: statusUpdated, mutation: mutationCommitted, attempts: 1},
		{slug: "retired", action: fleet.ActionDelete, status: statusDeleted, mutation: mutationCommitted, attempts: 1},
		{slug: "weekly", action: fleet.ActionUpdateSourceConfig, status: statusFailed, mutation: mutationPartial,
			attempts: 1, err: errors.New("config patch returned HTTP 500")},
		{slug: "forecast", action: fleet.ActionUpdateConfig, status: statusConflict, mutation: mutationNone,
			attempts: 1, err: errors.New("resource revision changed")},
	}}
	var out bytes.Buffer
	_ = renderApplyReportWithContext(&out, ctx, o, false)
	assertPlanWidth(t, out.String(), 120)
	assertTextGolden(t, "fleet_apply_partial.golden", out.Bytes())
}

func TestFleetApplyJSON_ExposesRunMutationAndRecoveryContract(t *testing.T) {
	ctx := applyReportContext{
		RunID: "run-json", FleetID: "eu", ResumeCommand: "shinyhub fleet apply -f fleet.toml",
	}
	diff := []fleet.AppDiff{{Slug: "half", Action: fleet.ActionUpdateSourceConfig}}
	result := applyResult{
		slug: "half", action: fleet.ActionUpdateSourceConfig, status: statusFailed,
		mutation: mutationPartial, err: errors.New("patch failed"),
	}
	var out bytes.Buffer
	if err := writeFleetApplyJSONWithContext(&out, ctx, &fleet.Manifest{FleetID: "eu"}, "https://h", diff, nil,
		applyOutcome{apps: []applyResult{result}}, 4, "PARTIAL"); err != nil {
		t.Fatal(err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.RunID != "run-json" || env.Status != "partial" {
		t.Fatalf("run identity/status = %q/%q", env.RunID, env.Status)
	}
	if got := env.Apps[0].Result; got == nil || got.MutationState != "partial" {
		t.Fatalf("per-resource mutation state missing: %+v", got)
	}
	if env.Recovery.Strategy != "repair_then_resume" || env.Recovery.SafeToRetry || len(env.Recovery.Commands) != 1 {
		t.Fatalf("recovery contract = %+v", env.Recovery)
	}
}

func TestApplyRecovery_OnlyRetriesTransientDeployFailures(t *testing.T) {
	ctx := applyReportContext{ResumeCommand: "shinyhub fleet apply"}
	transient := applyResult{status: statusFailed, deployFailed: true,
		attemptsDetail: []attemptOutcome{{Kind: deployfail.ServerError}}}
	if got := applyRecoveryFor(ctx, []applyResult{transient}); got.Strategy != "resume" || !got.SafeToRetry {
		t.Fatalf("transient recovery = %+v, want safe resume", got)
	}
	deterministic := applyResult{status: statusFailed, deployFailed: true,
		attemptsDetail: []attemptOutcome{{Kind: deployfail.BuildFailed}}}
	if got := applyRecoveryFor(ctx, []applyResult{deterministic}); got.Strategy != "repair_then_resume" || got.SafeToRetry {
		t.Fatalf("deterministic recovery = %+v, want repair then resume", got)
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
	if err := writeFleetApplyJSON(&out, m, "https://h", diff, nil, applyOutcome{apps: res}, 0, "OK - all converged"); err != nil {
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
		slug: "app-b", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
		deployFailed: true,
		err:          errors.New("deploy app-b failed: HTTP 500"),
		attemptsDetail: []attemptOutcome{
			{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"},
			{Attempt: 2, Kind: deployfail.Crashed, Err: "y"},
		},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", applyOutcome{apps: res}, false)
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
	_ = renderApplyReport(&buf, "eu", applyOutcome{apps: res}, false)
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
	d := fleet.AppDiff{Slug: "app-b", Action: fleet.ActionUpdateSource}
	r := applyResult{
		slug: "app-b", action: fleet.ActionUpdateSource, status: statusFailed, attempts: 2,
		deployFailed: true,
		err:          errors.New("boom"),
		attemptsDetail: []attemptOutcome{
			{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"},
			{Attempt: 2, Kind: deployfail.Crashed, Err: "y"},
		},
	}
	var buf bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, "http://h", []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 4, "PARTIAL"); err != nil {
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
	if err := writeFleetApplyJSON(&buf, m, "http://h", []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 0, "OK"); err != nil {
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

// A failure AFTER the deploy succeeded (config patch, or a first-fire after a
// retried-then-succeeded deploy) must NOT inherit a deploy attempt's kind at the
// top level, even though attempt_details may still record the earlier flake.
func TestWriteFleetApplyJSON_UnclassifiedPostDeployFailureOmitsFailureKind(t *testing.T) {
	d := fleet.AppDiff{Slug: "pp", Action: fleet.ActionUpdateSourceConfig}
	r := applyResult{
		slug: "pp", action: fleet.ActionUpdateSourceConfig, status: statusFailed, attempts: 2,
		err:            errors.New("patch boom"),
		deployFailed:   false,
		attemptsDetail: []attemptOutcome{{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"}},
	}
	var buf bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, "http://h", []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 4, "PARTIAL"); err != nil {
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
	if got.FailureKind != "" {
		t.Fatalf("post-deploy failure must omit the top-level failure_kind, got %q", got.FailureKind)
	}
	if len(got.AttemptDetails) != 1 || got.AttemptDetails[0].FailureKind != "readiness_timeout" {
		t.Fatalf("attempt_details should still record the deploy flake, got %+v", got.AttemptDetails)
	}
}

func TestWriteFleetApplyJSON_ScheduleGateFailureHasStableKind(t *testing.T) {
	d := fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged}
	r := applyResult{
		slug: "projects", action: fleet.ActionUnchanged, status: statusFailed,
		err: errors.New("schedule freshness gate unsatisfied"), failureKind: failureScheduleStale,
	}
	var buf bytes.Buffer
	if err := writeFleetApplyJSON(&buf, &fleet.Manifest{FleetID: "eu"}, "http://h", []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 4, "PARTIAL"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got := env.Apps[0].Result.FailureKind; got != failureScheduleStale {
		t.Fatalf("failure_kind = %q, want %q", got, failureScheduleStale)
	}
}

func TestRenderApplyReport_PostDeployFailureNoKindTag(t *testing.T) {
	res := []applyResult{{
		slug: "pp", action: fleet.ActionUpdateSourceConfig, status: statusFailed, attempts: 2,
		err:            errors.New("patch boom"),
		deployFailed:   false,
		attemptsDetail: []attemptOutcome{{Attempt: 1, Kind: deployfail.ReadinessTimeout, Err: "x"}},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", applyOutcome{apps: res}, false)
	out := buf.String()
	if strings.Contains(out, "failed [") {
		t.Fatalf("post-deploy failure must not tag the status with a deploy kind, got:\n%s", out)
	}
	if !strings.Contains(out, "attempt 1: readiness_timeout") {
		t.Fatalf("the deploy flake should still be listed, got:\n%s", out)
	}
}

func TestRenderApplyReport_WarmFailureUsesScheduleLogHint(t *testing.T) {
	res := []applyResult{{
		slug: "token-finops", action: fleet.ActionUnchanged, status: statusFailed,
		err: errors.New("warm gate already unsatisfied before this apply"),
		warmGate: []scheduleGateOutcome{{
			Schedule: "refresh-data", State: "never_succeeded", LastRunID: 703, LastRunStatus: "failed",
		}},
		scheduleLogs: []scheduleFailureLog{{
			Schedule: "refresh-data", RunID: 703, Tail: []string{"Traceback", "TABLE_NOT_FOUND"},
		}},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", applyOutcome{apps: res}, false)
	out := buf.String()
	for _, want := range []string{
		"warm gate unsatisfied before this apply (never succeeded; last run failed #703)",
		"TABLE_NOT_FOUND",
		"shinyhub schedule logs token-finops refresh-data --run 703",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "shinyhub apps logs token-finops") {
		t.Fatalf("warm failure must not point at the replica log:\n%s", out)
	}
}

func TestWriteFleetApplyJSON_DistinguishesStandingWarmFailure(t *testing.T) {
	d := fleet.AppDiff{Slug: "token-finops", Action: fleet.ActionUnchanged}
	r := applyResult{
		slug: "token-finops", action: fleet.ActionUnchanged, status: statusFailed,
		err: errors.New("warm gate already unsatisfied before this apply"),
		warmGate: []scheduleGateOutcome{{
			Schedule: "refresh-data", State: "never_succeeded", LastRunID: 703, LastRunStatus: "failed",
		}},
		scheduleLogs: []scheduleFailureLog{{Schedule: "refresh-data", RunID: 703, Tail: []string{"TABLE_NOT_FOUND"}}},
	}
	var buf bytes.Buffer
	if err := writeFleetApplyJSON(&buf, &fleet.Manifest{FleetID: "eu"}, "http://h", []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 4, "PARTIAL"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	var env applyJSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(env.Apps) != 1 || len(env.Apps[0].WarmGate) != 1 {
		t.Fatalf("warm_gate missing from app JSON: %+v", env.Apps)
	}
	if env.Apps[0].WarmGate[0].State != "never_succeeded" {
		t.Fatalf("warm gate = %+v", env.Apps[0].WarmGate[0])
	}
	if env.Apps[0].Result == nil || len(env.Apps[0].Result.ScheduleLogs) != 1 ||
		env.Apps[0].Result.ScheduleLogs[0].RunID != 703 {
		t.Fatalf("schedule_logs missing from result JSON: %+v", env.Apps[0].Result)
	}
}

func TestRenderApplyReport_StaleScheduleIsDistinct(t *testing.T) {
	res := []applyResult{{
		slug: "projects", action: fleet.ActionUnchanged, status: statusFailed,
		err: errors.New("schedule freshness gate unsatisfied"), failureKind: failureScheduleStale,
		freshnessGate: []scheduleGateOutcome{{
			Schedule: "refresh-pend-data", State: "stale", LastRunID: 804, LastRunStatus: "failed",
		}},
		scheduleLogs: []scheduleFailureLog{{Schedule: "refresh-pend-data", RunID: 804}},
	}}
	var buf bytes.Buffer
	_ = renderApplyReport(&buf, "eu", applyOutcome{apps: res}, false)
	out := buf.String()
	for _, want := range []string{
		"failed [schedule_stale]",
		"schedule freshness gate unsatisfied (stale; last run failed #804)",
		"shinyhub schedule logs projects refresh-pend-data --run 804",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}
