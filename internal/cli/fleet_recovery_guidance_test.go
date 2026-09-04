package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deployfail"
)

const testResumeCommand = "shinyhub fleet apply --restart-after-warm --verify-schedules -f fleet.toml"

func recoveryCtx() applyReportContext {
	return applyReportContext{
		RunID:         "run-1",
		PlanCommand:   "shinyhub fleet plan -f fleet.toml",
		ResumeCommand: testResumeCommand,
	}
}

func deployFailedResult(slug string, kind deployfail.Kind) applyResult {
	return applyResult{
		slug: slug, status: statusFailed, deployFailed: true,
		attemptsDetail: []attemptOutcome{{Attempt: 1, Kind: kind, Err: "boom"}},
	}
}

func staleResult(slug string, gates ...scheduleGateOutcome) applyResult {
	return applyResult{
		slug: slug, status: statusFailed,
		failureKind: failureScheduleStale, freshnessGate: gates,
	}
}

func joinCommands(r applyRecovery) string { return strings.Join(r.Commands, "\n") }

// A deferred no-downtime deploy is cleared by a decision, not a repair. The
// summary must name that decision and the flag that carries it, or the operator
// is told to "fix the failure" with nothing identified as broken.
func TestApplyRecovery_DowntimeDeferralNamesTheDecision(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{deployFailedResult("analytics", deployfail.DowntimeRequired)})
	if !strings.Contains(got.Summary, "--allow-downtime") {
		t.Errorf("summary must name the remedy flag: %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "working version") {
		t.Errorf("summary must say the running version was preserved: %q", got.Summary)
	}
	if got.SafeToRetry {
		t.Error("an unchanged re-run is refused identically; it is not safe to retry")
	}
	if strings.Contains(got.Summary, "Fix the reported") {
		t.Errorf("nothing is broken to fix; the operator chooses downtime: %q", got.Summary)
	}
}

// The reported production run hit both gates in one apply. Guidance for one
// must not swallow the other, and a failure with no specific remedy must keep
// the generic repair instruction.
func TestApplyRecovery_MixedFailuresKeepGenericAndSpecificGuidance(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		deployFailedResult("charts", deployfail.BuildFailed),
		deployFailedResult("analytics", deployfail.DowntimeRequired),
		staleResult("reporting", scheduleGateOutcome{Schedule: "refresh-data", State: "stale"}),
	})
	for _, want := range []string{"Fix the reported", "--allow-downtime", "schedule run"} {
		if !strings.Contains(got.Summary+"\n"+joinCommands(got), want) {
			t.Errorf("mixed-failure recovery missing %q, got summary %q and commands:\n%s",
				want, got.Summary, joinCommands(got))
		}
	}
}

// Consent for dropping live sessions is the operator's to give, exactly as
// --yes is never pre-filled into a recovery command. The flag belongs in the
// prose, never in a command built for copy-paste.
func TestApplyRecovery_DowntimeDeferralDoesNotPreconfirmDowntime(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{deployFailedResult("analytics", deployfail.DowntimeRequired)})
	if strings.Contains(joinCommands(got), "--allow-downtime") {
		t.Errorf("recovery command must not pre-confirm downtime: %q", joinCommands(got))
	}
	if !strings.Contains(joinCommands(got), testResumeCommand) {
		t.Errorf("recovery must still offer the plain resume command: %q", joinCommands(got))
	}
}

// The gate is cleared by running the producer, which apply deliberately will
// not do on its own. Offering only "re-run apply" sends the operator into a
// loop that fails identically every time.
func TestApplyRecovery_StaleScheduleNamesTheProducerRun(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		staleResult("analytics", scheduleGateOutcome{Schedule: "refresh-data", State: "stale"}),
		staleResult("reporting", scheduleGateOutcome{Schedule: "refresh-data", State: "stale"}),
	})
	for _, want := range []string{
		"shinyhub schedule run analytics refresh-data",
		"shinyhub schedule run reporting refresh-data",
		testResumeCommand,
	} {
		if !strings.Contains(joinCommands(got), want) {
			t.Errorf("recovery commands missing %q, got:\n%s", want, joinCommands(got))
		}
	}
}

// Producer runs come before the re-apply: running them afterwards leaves the
// gate exactly as unsatisfied as it was.
func TestApplyRecovery_ProducerRunsPrecedeTheResume(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		staleResult("analytics", scheduleGateOutcome{Schedule: "refresh-data", State: "stale"}),
	})
	run, resume := -1, -1
	for i, c := range got.Commands {
		if strings.Contains(c, "schedule run") {
			run = i
		}
		if strings.Contains(c, "fleet apply") {
			resume = i
		}
	}
	if run < 0 || resume < 0 {
		t.Fatalf("expected both a producer run and a resume command, got:\n%s", joinCommands(got))
	}
	if run > resume {
		t.Errorf("producer run must precede the resume, got:\n%s", joinCommands(got))
	}
}

// A refreshing schedule already has a run in flight. Dispatching a second one
// is either skipped as an overlap or duplicates expensive producer work; the
// operator needs to wait, not to fire again.
func TestApplyRecovery_RefreshingScheduleIsNotToldToRunAgain(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		staleResult("analytics", scheduleGateOutcome{Schedule: "refresh-data", State: "stale_refreshing", Refreshing: true}),
	})
	if strings.Contains(joinCommands(got), "schedule run") {
		t.Errorf("a refreshing schedule must not be told to run again: %q", joinCommands(got))
	}
}

// A repair obligation and an unreadable freshness state are not cleared by a
// producer run, so suggesting one sends the operator down the wrong path.
func TestApplyRecovery_NonProducerGatesGetNoRunCommand(t *testing.T) {
	for _, state := range []string{"producer_repair_required", "producer_mismatch", "unavailable"} {
		got := applyRecoveryFor(recoveryCtx(), []applyResult{
			staleResult("analytics", scheduleGateOutcome{Schedule: "refresh-data", State: state}),
		})
		if strings.Contains(joinCommands(got), "schedule run") {
			t.Errorf("state %q must not suggest a producer run: %q", state, joinCommands(got))
		}
	}
}

// One app can fail several freshness gates at once, in different states. A
// producer run clears one of them; the rest still need diagnosis. Counting the
// app as fully guided drops the generic repair line, and the unaddressed gate
// then appears nowhere at all - the operator runs the suggested command,
// re-applies, and fails again with no explanation.
func TestApplyRecovery_UnaddressedGateOnTheSameAppKeepsTheGenericRepair(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{
		staleResult("analytics",
			scheduleGateOutcome{Schedule: "refresh-data", State: "stale"},
			scheduleGateOutcome{Schedule: "audit-log", State: "producer_repair_required"}),
	})
	if !strings.Contains(got.Summary, "Fix the reported") {
		t.Errorf("an unaddressed gate must keep the generic repair instruction: %q", got.Summary)
	}
	// The guidance we can give is still given.
	if !strings.Contains(joinCommands(got), "shinyhub schedule run analytics refresh-data") {
		t.Errorf("the coverable gate must still get its producer run: %q", joinCommands(got))
	}
}

// The downtime deferral is only "fully guided" when nothing else on that app
// needs diagnosis either.
func TestApplyRecovery_DeferralWithAnUnaddressedGateKeepsTheGenericRepair(t *testing.T) {
	r := deployFailedResult("analytics", deployfail.DowntimeRequired)
	r.freshnessGate = []scheduleGateOutcome{{Schedule: "audit-log", State: "producer_mismatch"}}
	got := applyRecoveryFor(recoveryCtx(), []applyResult{r})
	if !strings.Contains(got.Summary, "Fix the reported") {
		t.Errorf("an unaddressed gate must keep the generic repair instruction: %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "--allow-downtime") {
		t.Errorf("the deferral guidance must survive alongside it: %q", got.Summary)
	}
}

// recoverySummaryLines returns the rendered Recovery prose: everything between
// the mutation counts and the first numbered command.
func recoverySummaryLines(report string) []string {
	var lines []string
	collecting := false
	for _, l := range strings.Split(report, "\n") {
		switch {
		case strings.Contains(l, "Remote mutation:"):
			collecting = true
		case collecting && regexp.MustCompile(`^\s+\d+\. `).MatchString(l):
			return lines
		case collecting && strings.TrimSpace(l) != "":
			lines = append(lines, l)
		}
	}
	return lines
}

// Guidance for several apps concatenates into prose far past a terminal line.
// It is the most important text in a failure report, so it wraps like every
// other prose block the CLI prints. Commands are deliberately excluded: a
// wrapped command is no longer copy-pasteable.
func TestApplyReport_RecoverySummaryWrapsToWidth(t *testing.T) {
	var out bytes.Buffer
	_ = renderApplyReportWithContext(&out, recoveryCtx(), applyOutcome{apps: []applyResult{
		deployFailedResult("analytics", deployfail.DowntimeRequired),
		staleResult("reporting", scheduleGateOutcome{Schedule: "refresh-data", State: "stale"}),
	}}, false)
	lines := recoverySummaryLines(out.String())
	if len(lines) < 2 {
		t.Fatalf("expected the summary to wrap onto several lines, got %d:\n%s", len(lines), out.String())
	}
	for _, l := range lines {
		if visibleWidth(l) > defaultPlanWidth {
			t.Errorf("summary line exceeds %d columns (%d): %q", defaultPlanWidth, visibleWidth(l), l)
		}
	}
	// Wrapping must not drop or mangle the guidance.
	joined := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	for _, want := range []string{"--allow-downtime", "Apply will not run producers itself"} {
		if !strings.Contains(joined, want) {
			t.Errorf("wrapped summary lost %q:\n%s", want, joined)
		}
	}
}

// The per-app line ends with the one most useful next command. A deferred
// deploy never started a process, so its app log holds nothing about the
// refusal; sending the operator there is the same wrong-path mistake as
// calling the refusal a conflict.
func TestApplyReport_DeferredDeployIsNotSentToTheAppLog(t *testing.T) {
	var out bytes.Buffer
	_ = renderApplyReport(&out, "eu", applyOutcome{
		apps: []applyResult{deployFailedResult("analytics", deployfail.DowntimeRequired)},
	}, false)
	if strings.Contains(out.String(), "apps logs analytics") {
		t.Errorf("a deferred deploy has no app log to read:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--allow-downtime") {
		t.Errorf("the report must name the remedy:\n%s", out.String())
	}
}

// Negative control for the line above: an ordinary deploy failure DOES leave a
// log worth reading, so the hint must survive.
func TestApplyReport_BuildFailureKeepsTheAppLogHint(t *testing.T) {
	var out bytes.Buffer
	_ = renderApplyReport(&out, "eu", applyOutcome{
		apps: []applyResult{deployFailedResult("analytics", deployfail.BuildFailed)},
	}, false)
	if !strings.Contains(out.String(), "apps logs analytics") {
		t.Errorf("a build failure must still point at the app log:\n%s", out.String())
	}
}

// Negative control: producer-run guidance must be scoped to the gate that
// needs it, not attached to every failed app.
func TestApplyRecovery_UnrelatedFailureGetsNoRunCommand(t *testing.T) {
	got := applyRecoveryFor(recoveryCtx(), []applyResult{deployFailedResult("analytics", deployfail.BuildFailed)})
	if strings.Contains(joinCommands(got), "schedule run") {
		t.Errorf("a build failure must not suggest a producer run: %q", joinCommands(got))
	}
}
