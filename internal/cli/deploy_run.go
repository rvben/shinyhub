package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// deployRunRef identifies a deploy-triggered run dispatched by the server.
type deployRunRef struct {
	Schedule   string
	ScheduleID int64
	RunID      int64
}

// deployRunOutcome is the per-schedule result the fleet report and JSON envelope
// surface. Status is empty when the CLI did not wait (default async path).
type deployRunOutcome struct {
	Schedule string `json:"schedule"`
	RunID    int64  `json:"run_id"`
	Status   string `json:"status,omitempty"`
}

// scheduleGateOutcome records a level check performed for an app. It is
// deliberately separate from deployRunOutcome: no run was triggered by this
// apply, and operators need to distinguish a standing never-succeeded state
// from a deploy-triggered run that failed during the current apply.
type scheduleGateOutcome struct {
	Schedule      string `json:"schedule"`
	State         string `json:"state"`
	Origin        string `json:"origin,omitempty"`
	LastRunID     int64  `json:"last_run_id,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	Refreshing    bool   `json:"refreshing,omitempty"`
	ActiveRunID   int64  `json:"active_run_id,omitempty"`
}

// scheduleFailureLog identifies the schedule-run log relevant to a warm-gate
// failure. Tail is best-effort; the identity is retained even when fetching
// the log fails so the report can still print the correct command.
type scheduleFailureLog struct {
	Schedule   string   `json:"schedule"`
	RunID      int64    `json:"run_id"`
	Tail       []string `json:"tail,omitempty"`
	FetchError string   `json:"fetch_error,omitempty"`
}

const (
	failureWarmWaitTimeout      = "warm_wait_timeout"
	failureWarmStateUnavailable = "warm_state_unavailable"
	failureWarmDeployRunFailed  = "warm_deploy_run_failed"
	failureWarmNeverSucceeded   = "warm_bundle_not_ready"
	failureWarmRestartFailed    = "warm_restart_failed"
	failureScheduleStale        = "schedule_stale"
	failureScheduleProducer     = "schedule_producer_mismatch"
	failureScheduleStateMissing = "schedule_state_unavailable"
)

// scheduleLogTailLines keeps enough of a typical Python/R traceback to include
// the terminal cause while keeping fleet output compact.
const scheduleLogTailLines = 25

// deployRunRefsFromDeployResponse extracts deploy-triggered run references.
// The internal name is retained locally because this file also owns the shared
// polling machinery. The authoritative wire contract is the top-level
// schedule_convergence array; nested deploy_run is accepted as a fallback.
func deployRunRefsFromDeployResponse(body []byte) []deployRunRef {
	var resp struct {
		ScheduleConvergence []struct {
			Schedule   string `json:"schedule"`
			ScheduleID int64  `json:"schedule_id"`
			RunID      *int64 `json:"run_id"`
			Prestart   bool   `json:"prestart"`
		} `json:"schedule_convergence"`
		Manifest struct {
			Schedules []struct {
				Name       string `json:"name"`
				ScheduleID int64  `json:"schedule_id"`
				DeployRun  *struct {
					RunID int64 `json:"run_id"`
				} `json:"deploy_run"`
			} `json:"schedules"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	var refs []deployRunRef
	seen := map[int64]bool{}
	for _, convergence := range resp.ScheduleConvergence {
		seen[convergence.ScheduleID] = true
		if convergence.RunID == nil || convergence.Prestart {
			continue
		}
		refs = append(refs, deployRunRef{Schedule: convergence.Schedule, ScheduleID: convergence.ScheduleID, RunID: *convergence.RunID})
	}
	for _, s := range resp.Manifest.Schedules {
		if s.DeployRun != nil && !seen[s.ScheduleID] {
			refs = append(refs, deployRunRef{Schedule: s.Name, ScheduleID: s.ScheduleID, RunID: s.DeployRun.RunID})
		}
	}
	return refs
}

// errDeployRunTimeout is returned by waitForDeployRunLoop when the run does not
// reach a terminal state within the timeout.
var errDeployRunTimeout = errors.New("deploy-triggered run wait timed out")

// deployRunStatusOK reports whether a terminal run status proves the cache was
// warmed. An overlap skip proves only that another process exists, never that
// data was successfully delivered.
func deployRunStatusOK(status string) bool {
	return status == "succeeded"
}

// waitForDeployRunLoop polls the run's status until it leaves "running" or the
// timeout elapses, emitting a progress line every progressEvery. now and sleep
// are injected so the cadence is deterministic in tests. It returns the last
// observed status; on timeout it also returns errDeployRunTimeout. Transient
// poll errors (5xx / transport) are retried until the deadline; a fatal 4xx
// aborts immediately.
func waitForDeployRunLoop(poll func() (string, error), timeout, pollEvery, progressEvery time.Duration,
	now func() time.Time, sleep func(time.Duration), out io.Writer, label string) (string, error) {
	s := stylerFor(out)
	start := now()
	deadline := start.Add(timeout)
	lastProgress := start
	lastStatus := "running"
	for {
		status, err := poll()
		// Measure the deadline after the request returns. A context-aware poll can
		// consume the entire remaining budget; using its start time here would
		// sleep that budget a second time before noticing the timeout.
		t := now()
		if err == nil {
			lastStatus = status
			if status != "running" {
				return status, nil
			}
		} else {
			var he *deployHTTPError
			if errors.As(err, &he) && he.fatal() {
				return lastStatus, err
			}
			// transient (5xx / transport): keep looping until the deadline
		}
		if !t.Before(deadline) {
			return lastStatus, errDeployRunTimeout
		}
		if t.Sub(lastProgress) >= progressEvery {
			// Yellow rather than styler.status("running"): here the word means a
			// job still in flight, not the steady healthy state that status()
			// paints green. Green would read as "this finished successfully".
			fmt.Fprintf(out, "  %s: deploy-triggered run still %s %s\n", label, s.yellow("running"),
				s.dim(fmt.Sprintf("(%s/%s)", t.Sub(start).Round(time.Second), timeout)))
			lastProgress = t
		}
		delay := pollEvery
		if remaining := deadline.Sub(t); remaining < delay {
			delay = remaining
		}
		if delay > 0 {
			sleep(delay)
		}
	}
}

// pollScheduleRunStatus fetches GET /api/apps/{slug}/schedules/{id}/runs/{run}
// and returns the run's status string.
func pollScheduleRunStatus(cfg *cliConfig, slug string, scheduleID, runID int64) (string, error) {
	return pollScheduleRunStatusContext(context.Background(), cfg, slug, scheduleID, runID)
}

func pollScheduleRunStatusContext(ctx context.Context, cfg *cliConfig, slug string, scheduleID, runID int64) (string, error) {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d", cfg.Host, slug, scheduleID, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &deployHTTPError{statusCode: resp.StatusCode, body: string(body)}
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return "", err
	}
	return run.Status, nil
}

func appendScheduleLog(cfg *cliConfig, slug string, scheduleID, runID int64, schedule string, res *applyResult) {
	tail, err := fetchScheduleLogTail(cfg, slug, scheduleID, runID, scheduleLogTailLines)
	entry := scheduleFailureLog{Schedule: schedule, RunID: runID, Tail: tail}
	if err != nil {
		entry.FetchError = err.Error()
	}
	res.scheduleLogs = append(res.scheduleLogs, entry)
}

func scheduleStatusHasFailureLog(status string) bool {
	switch status {
	case "failed", "timed_out", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

// fetchScheduleLogTail returns the last n non-empty lines of one schedule run.
// Unlike an app process log, this is where the scheduled command's traceback
// and exit reason are recorded.
func fetchScheduleLogTail(cfg *cliConfig, slug string, scheduleID, runID int64, n int) ([]string, error) {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d/logs?follow=false", cfg.Host, slug, scheduleID, runID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return parsePlainLines(resp.Body, n), nil
}

// verifyExistingWarmGate evaluates server-owned deploy-trigger convergence
// after every non-delete action. The reconcile request repairs missing
// admission first; waiting controls patience, not whether required work exists.
func verifyExistingWarmGate(cfg *cliConfig, slug, bundleDir string, res *applyResult) error {
	return verifyExistingWarmGateWithWait(cfg, slug, bundleDir, res, 0, io.Discard)
}

// verifyExistingWarmGateWithWait extends the level check by joining an active
// run when the caller supplied a deadline. This makes a retried apply patient
// with work that is already repairing the condition, without dispatching a
// duplicate query or accepting "running" as proof of delivered data.
func verifyExistingWarmGateWithWait(cfg *cliConfig, slug, bundleDir string, res *applyResult, timeout time.Duration, out io.Writer) error {
	if err := requestScheduleConvergence(cfg, slug); err != nil {
		res.failureKind = failureWarmStateUnavailable
		return fmt.Errorf("reconcile deploy-triggered schedules: %w", err)
	}
	remote, err := listSchedules(cfg, slug)
	if err != nil {
		res.failureKind = failureWarmStateUnavailable
		return fmt.Errorf("verify deploy-triggered schedules: %w", err)
	}
	declared := make([]string, 0, len(remote))
	byName := make(map[string]scheduleDTO, len(remote))
	for _, schedule := range remote {
		byName[schedule.Name] = schedule
		if schedule.Enabled && schedule.DeployTrigger != "never" {
			declared = append(declared, schedule.Name)
		}
	}
	if len(declared) == 0 {
		if err := requireAppCompatibilityClear(cfg, slug); err != nil {
			classifyAppCompatibilityFailure(res, err)
			return err
		}
		return nil
	}

	var failures []string
	var ctx context.Context
	var cancel context.CancelFunc
	var deadline time.Time
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
		deadline = time.Now().Add(timeout)
	}
	origin := warmGateOrigin(res.action)
	for _, name := range declared {
		schedule, ok := byName[name]
		if !ok {
			res.warmGate = append(res.warmGate, scheduleGateOutcome{Schedule: name, State: "missing", Origin: origin})
			failures = append(failures, fmt.Sprintf("schedule %q is missing", name))
			continue
		}
		if schedule.DeployTriggerSatisfied == nil {
			res.failureKind = failureWarmStateUnavailable
			return fmt.Errorf("verify schedule %q: server did not report deploy-trigger convergence", name)
		}
		if *schedule.DeployTriggerSatisfied {
			continue
		}
		repairRequired := schedule.ProducerRepairRequired != nil && *schedule.ProducerRepairRequired

		outcome := scheduleGateOutcome{Schedule: name, State: "bundle_not_ready", Origin: origin}
		if schedule.LastRunStatus != nil {
			outcome.LastRunStatus = *schedule.LastRunStatus
		}
		if schedule.LastRunAt != nil {
			outcome.LastRunAt = *schedule.LastRunAt
		}
		if schedule.LastSuccessAt != nil {
			outcome.LastSuccessAt = *schedule.LastSuccessAt
		}

		if schedule.LastRunID != nil {
			outcome.LastRunID = *schedule.LastRunID
		}
		if schedule.Refreshing != nil {
			outcome.Refreshing = *schedule.Refreshing
		}
		if schedule.ConvergenceRunID != nil && schedule.ConvergenceStatus == "running" {
			outcome.ActiveRunID = *schedule.ConvergenceRunID
			outcome.Refreshing = true
		}
		logCount := len(res.scheduleLogs)
		if outcome.Refreshing && outcome.ActiveRunID > 0 && timeout > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				res.warmGate = append(res.warmGate, outcome)
				res.failureKind = failureWarmWaitTimeout
				appendScheduleLog(cfg, slug, schedule.ID, outcome.ActiveRunID, name, res)
				return fmt.Errorf("schedule %q active warm-up not confirmed within --warm-timeout %s: %w", name, timeout, errDeployRunTimeout)
			}
			poll := func() (string, error) {
				return pollScheduleRunStatusContext(ctx, cfg, slug, schedule.ID, outcome.ActiveRunID)
			}
			status, waitErr := waitForDeployRunLoop(poll, remaining, 2*time.Second, fleetHealthProgressInterval,
				time.Now, time.Sleep, out, fleetDeployRunLabel(slug, name)+" (active)")
			if waitErr != nil {
				res.warmGate = append(res.warmGate, outcome)
				res.failureKind = failureWarmStateUnavailable
				if errors.Is(waitErr, errDeployRunTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					res.failureKind = failureWarmWaitTimeout
				}
				appendScheduleLog(cfg, slug, schedule.ID, outcome.ActiveRunID, name, res)
				return fmt.Errorf("schedule %q active warm-up not confirmed within --warm-timeout %s: %w", name, timeout, waitErr)
			}
			if deployRunStatusOK(status) {
				latest, refreshErr := listSchedules(cfg, slug)
				if refreshErr != nil {
					res.failureKind = failureWarmStateUnavailable
					return fmt.Errorf("refresh schedule %q convergence: %w", name, refreshErr)
				}
				for _, candidate := range latest {
					if candidate.ID == schedule.ID && candidate.DeployTriggerSatisfied != nil && *candidate.DeployTriggerSatisfied {
						outcome.State = "converged"
						outcome.Refreshing = false
						res.warmGate = append(res.warmGate, outcome)
						goto nextSchedule
					}
				}
			}
			outcome.LastRunID = outcome.ActiveRunID
			outcome.LastRunStatus = status
			outcome.Refreshing = false
			if scheduleStatusHasFailureLog(status) {
				appendScheduleLog(cfg, slug, schedule.ID, outcome.ActiveRunID, name, res)
			}
		}
		if outcome.LastRunID > 0 && scheduleStatusHasFailureLog(outcome.LastRunStatus) && len(res.scheduleLogs) == logCount {
			appendScheduleLog(cfg, slug, schedule.ID, outcome.LastRunID, name, res)
		}
		res.warmGate = append(res.warmGate, outcome)
		failures = append(failures, describeDeployTriggerUnsatisfied(outcome, schedule, repairRequired, time.Now()))
	nextSchedule:
	}
	if len(failures) == 0 {
		// The per-schedule waits above are sequential. A concurrent deployment or
		// schedule edit can invalidate an item already checked, so success requires
		// one final authoritative whole-set snapshot. If that snapshot moved, loop
		// within the original remaining budget until the complete current set is a
		// fixed point; never combine successes from different deployments.
		latest, refreshErr := listSchedules(cfg, slug)
		if refreshErr != nil {
			res.failureKind = failureWarmStateUnavailable
			return fmt.Errorf("final schedule convergence check: %w", refreshErr)
		}
		var moved []string
		for _, schedule := range latest {
			if !schedule.Enabled || schedule.DeployTrigger == "never" {
				continue
			}
			if schedule.DeployTriggerSatisfied == nil {
				res.failureKind = failureWarmStateUnavailable
				return fmt.Errorf("verify schedule %q: server did not report deploy-trigger convergence", schedule.Name)
			}
			if !*schedule.DeployTriggerSatisfied {
				moved = append(moved, schedule.Name)
			}
		}
		if len(moved) == 0 {
			if err := requireAppCompatibilityClear(cfg, slug); err != nil {
				classifyAppCompatibilityFailure(res, err)
				return err
			}
			return nil
		}
		if timeout <= 0 {
			res.failureKind = failureWarmNeverSucceeded
			return fmt.Errorf("schedule convergence changed during verification: %s", strings.Join(moved, ", "))
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			res.failureKind = failureWarmWaitTimeout
			return fmt.Errorf("schedule convergence changed during verification and was not restored within --warm-timeout %s: %w", timeout, errDeployRunTimeout)
		}
		return verifyExistingWarmGateWithWait(cfg, slug, bundleDir, res, remaining, out)
	}
	res.failureKind = failureWarmNeverSucceeded
	prefix := "warm gate already unsatisfied before this apply"
	if origin == "current_apply" {
		prefix = "warm gate unsatisfied after this apply"
	}
	return fmt.Errorf("%s: %s", prefix, strings.Join(failures, "; "))
}

type appCompatibilityQuarantineError struct{ detail string }

func (e *appCompatibilityQuarantineError) Error() string { return e.detail }

func classifyAppCompatibilityFailure(res *applyResult, err error) {
	var quarantine *appCompatibilityQuarantineError
	if errors.As(err, &quarantine) {
		res.failureKind = failureWarmNeverSucceeded
		return
	}
	res.failureKind = failureWarmStateUnavailable
}

// requireAppCompatibilityClear closes the gap between per-schedule convergence
// and the app-level consumer-start guard. A disabled uncertain producer or a
// failed deployment barrier may quarantine the app even when no enabled
// deploy-trigger schedule appears in the schedule list.
func requireAppCompatibilityClear(cfg *cliConfig, slug string) error {
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("verify app compatibility: build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify app compatibility: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("verify app compatibility: %w", &deployHTTPError{statusCode: resp.StatusCode, body: string(body)})
	}
	var state struct {
		CompatibilityQuarantined *bool `json:"compatibility_quarantined"`
		ProducerRepairRequired   *bool `json:"producer_repair_required"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return fmt.Errorf("verify app compatibility: decode response: %w", err)
	}
	if state.CompatibilityQuarantined == nil || state.ProducerRepairRequired == nil {
		return fmt.Errorf("verify app compatibility: server response omitted authoritative compatibility state")
	}
	switch {
	case *state.ProducerRepairRequired:
		return &appCompatibilityQuarantineError{detail: "app compatibility quarantine requires a successful producer repair before consumers can start"}
	case *state.CompatibilityQuarantined:
		return &appCompatibilityQuarantineError{detail: "app compatibility is quarantined by an incomplete producer barrier; consumers cannot start"}
	default:
		return nil
	}
}

func observedScheduleConvergenceWork(res applyResult) bool {
	if len(res.deployRuns) > 0 {
		return true
	}
	for _, gate := range res.warmGate {
		if gate.ActiveRunID > 0 || gate.State == "converged" {
			return true
		}
	}
	return false
}

func requestScheduleConvergence(cfg *cliConfig, slug string) error {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/reconcile", cfg.Host, slug)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &deployHTTPError{statusCode: resp.StatusCode, body: string(body)}
	}
	return nil
}

func describeDeployTriggerUnsatisfied(outcome scheduleGateOutcome, schedule scheduleDTO, repairRequired bool, now time.Time) string {
	if repairRequired {
		detail := fmt.Sprintf("schedule %q requires producer repair because a prior data write may be incomplete", outcome.Schedule)
		if outcome.LastRunStatus != "" {
			detail += "; " + describeNeverSucceeded(outcome, now)
		}
		return detail
	}
	detail := fmt.Sprintf("schedule %q producer convergence is not satisfied for app version %q (digest %s)",
		outcome.Schedule, schedule.CurrentAppVersion, schedule.CurrentContentDigest)
	if schedule.ProducerContentDigest != "" {
		detail += fmt.Sprintf("; current producer state is version %q (digest %s)",
			schedule.ProducerAppVersion, schedule.ProducerContentDigest)
	}
	if outcome.LastRunStatus != "" {
		detail += "; " + describeNeverSucceeded(outcome, now)
	}
	return detail
}

func warmGateOrigin(action fleet.Action) string {
	switch action {
	case fleet.ActionCreate, fleet.ActionAdopt, fleet.ActionUpdateSource, fleet.ActionUpdateSourceConfig:
		return "current_apply"
	default:
		return "pre_existing"
	}
}

func verifyPostDeployWarmGate(cfg *cliConfig, slug, bundleDir string, timeout time.Duration, out io.Writer) (applyResult, error) {
	res := applyResult{slug: slug, action: fleet.ActionUpdateSource}
	err := verifyExistingWarmGateWithWait(cfg, slug, bundleDir, &res, timeout, out)
	return res, err
}

func describeNeverSucceeded(outcome scheduleGateOutcome, now time.Time) string {
	detail := fmt.Sprintf("schedule %q has never succeeded", outcome.Schedule)
	if outcome.LastRunStatus == "" {
		return detail + " (no runs recorded)"
	}
	detail += fmt.Sprintf(" (last run %s", outcome.LastRunStatus)
	if outcome.Refreshing {
		detail += ", refreshing"
	}
	if outcome.LastRunID > 0 {
		detail += fmt.Sprintf(", run #%d", outcome.LastRunID)
	}
	if outcome.LastRunAt != "" {
		if at, err := time.Parse(time.RFC3339Nano, outcome.LastRunAt); err == nil {
			age := now.Sub(at)
			if age < 0 {
				age = 0
			}
			detail += ", " + humanizeAgeSeconds(float64(age/time.Second)) + " ago"
		}
	}
	return detail + ")"
}

// verifyEnabledScheduleFreshness implements the opt-in read-only schedule
// gate. Every enabled schedule must satisfy cron freshness and every enabled
// deploy-trigger policy must match the authoritative producer state. It
// consumes the API's answers instead of duplicating server policy in the CLI.
func verifyEnabledScheduleFreshness(cfg *cliConfig, slug string, res *applyResult) error {
	schedules, err := listSchedules(cfg, slug)
	if err != nil {
		res.failureKind = failureScheduleStateMissing
		return fmt.Errorf("verify schedule freshness: %w", err)
	}
	var failures []string
	producerMismatch := false
	for _, schedule := range schedules {
		repairRequired := schedule.ProducerRepairRequired != nil && *schedule.ProducerRepairRequired
		// Disabling a producer cannot undo a partial physical write. Durable
		// uncertainty is app-level compatibility state and remains part of this
		// safety gate until a producer repair succeeds.
		if !schedule.Enabled && !repairRequired {
			continue
		}
		stale := false
		if schedule.Enabled && schedule.Stale == nil {
			res.freshnessGate = append(res.freshnessGate, scheduleGateOutcome{Schedule: schedule.Name, State: "unavailable"})
			res.failureKind = failureScheduleStateMissing
			return fmt.Errorf("verify schedule freshness: server did not report stale state for schedule %q; upgrade the server or omit --verify-schedules", schedule.Name)
		}
		if schedule.Enabled {
			stale = *schedule.Stale
		}
		deployMismatch := repairRequired
		if schedule.Enabled && schedule.DeployTrigger != "" && schedule.DeployTrigger != "never" {
			if schedule.DeployTriggerSatisfied == nil {
				res.freshnessGate = append(res.freshnessGate, scheduleGateOutcome{Schedule: schedule.Name, State: "unavailable"})
				res.failureKind = failureScheduleStateMissing
				return fmt.Errorf("verify schedule convergence: server did not report producer state for schedule %q", schedule.Name)
			}
			deployMismatch = deployMismatch || !*schedule.DeployTriggerSatisfied
		}
		if !stale && !deployMismatch {
			continue
		}

		outcome := scheduleGateOutcome{Schedule: schedule.Name}
		switch {
		case stale && repairRequired:
			outcome.State = "stale_producer_repair_required"
		case stale && deployMismatch:
			outcome.State = "stale_producer_mismatch"
		case stale:
			outcome.State = "stale"
		case repairRequired:
			outcome.State = "producer_repair_required"
		default:
			outcome.State = "producer_mismatch"
		}
		if stale && schedule.Refreshing != nil && *schedule.Refreshing {
			outcome.State = "stale_refreshing"
			if repairRequired {
				outcome.State = "stale_refreshing_producer_repair_required"
			} else if deployMismatch {
				outcome.State = "stale_refreshing_producer_mismatch"
			}
			outcome.Refreshing = true
		}
		if schedule.LastRunID != nil {
			outcome.LastRunID = *schedule.LastRunID
		}
		if schedule.LastRunStatus != nil {
			outcome.LastRunStatus = *schedule.LastRunStatus
		}
		if schedule.LastRunAt != nil {
			outcome.LastRunAt = *schedule.LastRunAt
		}
		if schedule.LastSuccessAt != nil {
			outcome.LastSuccessAt = *schedule.LastSuccessAt
		}
		// A successful-but-old run or a currently running refresh does not have
		// a failure traceback. Only attach logs when this exact atomic snapshot
		// identifies a terminal unsuccessful run.
		if outcome.LastRunID > 0 && scheduleStatusHasFailureLog(outcome.LastRunStatus) {
			appendScheduleLog(cfg, slug, schedule.ID, outcome.LastRunID, schedule.Name, res)
		}
		res.freshnessGate = append(res.freshnessGate, outcome)
		detail := ""
		if stale {
			detail = describeStaleSchedule(outcome, time.Now())
		}
		if deployMismatch {
			producerMismatch = true
			producerDetail := describeDeployTriggerUnsatisfied(outcome, schedule, repairRequired, time.Now())
			if detail != "" {
				detail += "; " + producerDetail
			} else {
				detail = producerDetail
			}
		}
		failures = append(failures, detail)
	}
	if len(failures) == 0 {
		return nil
	}
	res.failureKind = failureScheduleStale
	if producerMismatch {
		res.failureKind = failureScheduleProducer
	}
	return fmt.Errorf("schedule verification gate unsatisfied: %s", strings.Join(failures, "; "))
}

func describeStaleSchedule(outcome scheduleGateOutcome, now time.Time) string {
	detail := fmt.Sprintf("schedule %q is stale", outcome.Schedule)
	if outcome.Refreshing {
		detail += " and refreshing"
	}
	if outcome.LastRunStatus == "" {
		return detail + " (no runs recorded)"
	}
	detail += fmt.Sprintf(" (last run %s", outcome.LastRunStatus)
	if outcome.LastRunID > 0 {
		detail += fmt.Sprintf(", run #%d", outcome.LastRunID)
	}
	if outcome.LastRunAt != "" {
		if at, err := time.Parse(time.RFC3339Nano, outcome.LastRunAt); err == nil {
			age := now.Sub(at)
			if age < 0 {
				age = 0
			}
			detail += ", " + humanizeAgeSeconds(float64(age/time.Second)) + " ago"
		}
	}
	return detail + ")"
}

// restartAppAfterWarm cycles a serving app after every deploy-triggered run completed so
// a process that loaded data at startup gets a post-warm view. An explicitly
// stopped app stays stopped: its next start will already see the warmed data.
func restartAppAfterWarm(cfg *cliConfig, slug string, out io.Writer) (bool, error) {
	_, status, err := pollAppStatus(cfg, slug)
	if err != nil {
		return false, fmt.Errorf("check app before warm restart: %w", err)
	}
	if status == "stopped" {
		fmt.Fprintf(out, "%s: warm-up completed; app remains stopped\n", slug)
		return false, nil
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/restart", nil)
	if err != nil {
		return false, fmt.Errorf("build warm restart request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("restart after warm: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, httpError(cfg.Token, "restart after warm", resp, body)
	}
	fmt.Fprintf(out, "%s: restarted after bundle data convergence\n", slug)
	return true, nil
}
