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

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
)

// firstFireRef identifies a run_on_register first-fire dispatched by the server
// during a deploy, parsed from the deploy response's manifest.schedules[].
type firstFireRef struct {
	Schedule   string
	ScheduleID int64
	RunID      int64
}

// firstFireOutcome is the per-schedule result the fleet report and JSON envelope
// surface. Status is empty when the CLI did not wait (default async path).
type firstFireOutcome struct {
	Schedule string `json:"schedule"`
	RunID    int64  `json:"run_id"`
	Status   string `json:"status,omitempty"`
}

// scheduleGateOutcome records a level check performed for an app. It is
// deliberately separate from firstFireOutcome: no run was triggered by this
// apply, and operators need to distinguish a standing never-succeeded state
// from a first-fire that failed during the current apply.
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
	failureWarmFirstFireFailed  = "warm_first_fire_failed"
	failureWarmNeverSucceeded   = "warm_never_succeeded"
	failureWarmRestartFailed    = "warm_restart_failed"
	failureScheduleStale        = "schedule_stale"
	failureScheduleStateMissing = "schedule_state_unavailable"
)

// scheduleLogTailLines keeps enough of a typical Python/R traceback to include
// the terminal cause while keeping fleet output compact.
const scheduleLogTailLines = 25

// firstFireRefsFromDeployResponse extracts the first-fire references from a raw
// deploy response body. It returns an empty slice when no schedule was first-
// fired (the common case), so callers can range over it unconditionally.
func firstFireRefsFromDeployResponse(body []byte) []firstFireRef {
	var resp struct {
		Manifest struct {
			Schedules []struct {
				Name       string `json:"name"`
				ScheduleID int64  `json:"schedule_id"`
				FirstFire  *struct {
					RunID int64 `json:"run_id"`
				} `json:"first_fire"`
			} `json:"schedules"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	var refs []firstFireRef
	for _, s := range resp.Manifest.Schedules {
		if s.FirstFire != nil {
			refs = append(refs, firstFireRef{Schedule: s.Name, ScheduleID: s.ScheduleID, RunID: s.FirstFire.RunID})
		}
	}
	return refs
}

// errFirstFireTimeout is returned by waitForFirstFireLoop when the run does not
// reach a terminal state within the timeout.
var errFirstFireTimeout = errors.New("first-fire wait timed out")

// firstFireStatusOK reports whether a terminal run status proves the cache was
// warmed. An overlap skip proves only that another process exists, never that
// data was successfully delivered.
func firstFireStatusOK(status string) bool {
	return status == "succeeded"
}

// waitForFirstFireLoop polls the run's status until it leaves "running" or the
// timeout elapses, emitting a progress line every progressEvery. now and sleep
// are injected so the cadence is deterministic in tests. It returns the last
// observed status; on timeout it also returns errFirstFireTimeout. Transient
// poll errors (5xx / transport) are retried until the deadline; a fatal 4xx
// aborts immediately.
func waitForFirstFireLoop(poll func() (string, error), timeout, pollEvery, progressEvery time.Duration,
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
			return lastStatus, errFirstFireTimeout
		}
		if t.Sub(lastProgress) >= progressEvery {
			// Yellow rather than styler.status("running"): here the word means a
			// job still in flight, not the steady healthy state that status()
			// paints green. Green would read as "this finished successfully".
			fmt.Fprintf(out, "  %s: first-fire still %s %s\n", label, s.yellow("running"),
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

type scheduleRunBrief struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

// verifyExistingWarmGate evaluates run_on_register as a convergence level after
// every non-delete action. It therefore catches both standing failures and a
// current registration whose best-effort dispatch produced no first-fire ref.
// It performs no remote work when the manifest has no enabled run_on_register
// schedule and never triggers a run.
func verifyExistingWarmGate(cfg *cliConfig, slug, bundleDir string, res *applyResult) error {
	return verifyExistingWarmGateWithWait(cfg, slug, bundleDir, res, 0, io.Discard)
}

// verifyExistingWarmGateWithWait extends the level check by joining an active
// run when the caller supplied a deadline. This makes a retried apply patient
// with work that is already repairing the condition, without dispatching a
// duplicate query or accepting "running" as proof of delivered data.
func verifyExistingWarmGateWithWait(cfg *cliConfig, slug, bundleDir string, res *applyResult, timeout time.Duration, out io.Writer) error {
	m, err := deploy.LoadManifest(bundleDir)
	if err != nil {
		res.failureKind = failureWarmStateUnavailable
		return fmt.Errorf("verify warm gate: %w", err)
	}
	if m == nil {
		return nil
	}
	declared := make([]string, 0, len(m.Schedules))
	for _, spec := range m.Schedules {
		if spec.RunOnRegister && !spec.Disabled {
			declared = append(declared, spec.Name)
		}
	}
	if len(declared) == 0 {
		return nil
	}

	remote, err := listSchedules(cfg, slug)
	if err != nil {
		res.failureKind = failureWarmStateUnavailable
		return fmt.Errorf("verify run_on_register schedules: %w", err)
	}
	byName := make(map[string]scheduleDTO, len(remote))
	for _, schedule := range remote {
		byName[schedule.Name] = schedule
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
		if schedule.LastSuccessAt != nil && *schedule.LastSuccessAt != "" {
			continue
		}

		outcome := scheduleGateOutcome{Schedule: name, State: "never_succeeded", Origin: origin}
		if schedule.LastRunStatus != nil {
			outcome.LastRunStatus = *schedule.LastRunStatus
		}
		if schedule.LastRunAt != nil {
			outcome.LastRunAt = *schedule.LastRunAt
		}
		if schedule.LastSuccessAt != nil {
			outcome.LastSuccessAt = *schedule.LastSuccessAt
		}

		// stale is the capability marker for the atomic freshness fields. On a
		// current server, consume the run identity from the same snapshot as the
		// success decision. For older servers, scan run history until a success
		// is found rather than mistaking "latest run failed" for "never ran
		// successfully".
		if schedule.Stale != nil {
			if schedule.LastRunID != nil {
				outcome.LastRunID = *schedule.LastRunID
			}
			if schedule.Refreshing != nil {
				outcome.Refreshing = *schedule.Refreshing
			}
			if schedule.ActiveRunID != nil {
				outcome.ActiveRunID = *schedule.ActiveRunID
			}
		} else {
			succeeded, latest, lerr := legacyScheduleSuccessState(cfg, slug, schedule.ID)
			if lerr != nil {
				res.failureKind = failureWarmStateUnavailable
				return fmt.Errorf("verify schedule %q run history: %w", name, lerr)
			}
			if succeeded {
				continue
			}
			if latest != nil {
				outcome.LastRunID = latest.ID
				if outcome.LastRunStatus == "" {
					outcome.LastRunStatus = latest.Status
				}
				if outcome.LastRunAt == "" {
					outcome.LastRunAt = latest.StartedAt
				}
			}
		}
		logCount := len(res.scheduleLogs)
		if outcome.Refreshing && outcome.ActiveRunID > 0 && timeout > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				res.warmGate = append(res.warmGate, outcome)
				res.failureKind = failureWarmWaitTimeout
				appendScheduleLog(cfg, slug, schedule.ID, outcome.ActiveRunID, name, res)
				return fmt.Errorf("schedule %q active warm-up not confirmed within --warm-timeout %s: %w", name, timeout, errFirstFireTimeout)
			}
			poll := func() (string, error) {
				return pollScheduleRunStatusContext(ctx, cfg, slug, schedule.ID, outcome.ActiveRunID)
			}
			status, waitErr := waitForFirstFireLoop(poll, remaining, 2*time.Second, fleetHealthProgressInterval,
				time.Now, time.Sleep, out, fleetFirstFireLabel(slug, name)+" (active)")
			if waitErr != nil {
				res.warmGate = append(res.warmGate, outcome)
				res.failureKind = failureWarmStateUnavailable
				if errors.Is(waitErr, errFirstFireTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					res.failureKind = failureWarmWaitTimeout
				}
				appendScheduleLog(cfg, slug, schedule.ID, outcome.ActiveRunID, name, res)
				return fmt.Errorf("schedule %q active warm-up not confirmed within --warm-timeout %s: %w", name, timeout, waitErr)
			}
			if firstFireStatusOK(status) {
				continue
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
		failures = append(failures, describeNeverSucceeded(outcome, time.Now()))
	}
	if len(failures) == 0 {
		return nil
	}
	res.failureKind = failureWarmNeverSucceeded
	prefix := "warm gate already unsatisfied before this apply"
	if origin == "current_apply" {
		prefix = "warm gate unsatisfied after this apply"
	}
	return fmt.Errorf("%s: %s", prefix, strings.Join(failures, "; "))
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

// legacyScheduleSuccessState provides an honest fallback for servers predating
// atomic last_success_at/stale fields. It scans bounded pages until it either
// finds a success or exhausts history; inspecting only the newest run would
// falsely report "never succeeded" after a later failure.
func legacyScheduleSuccessState(cfg *cliConfig, slug string, scheduleID int64) (bool, *scheduleRunBrief, error) {
	const pageSize = 100
	var latest *scheduleRunBrief
	for offset := 0; ; offset += pageSize {
		url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs?limit=%d&offset=%d", cfg.Host, slug, scheduleID, pageSize, offset)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return false, latest, err
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return false, latest, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return false, latest, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return false, latest, httpError(cfg.Token, "list schedule runs", resp, body)
		}
		var env struct {
			Items []scheduleRunBrief `json:"items"`
			Total int                `json:"total"`
		}
		if err := json.Unmarshal(body, &env); err == nil && env.Items != nil {
			if latest == nil && len(env.Items) > 0 {
				v := env.Items[0]
				latest = &v
			}
			for _, run := range env.Items {
				if run.Status == "succeeded" {
					return true, latest, nil
				}
			}
			if len(env.Items) < pageSize || (env.Total > 0 && offset+len(env.Items) >= env.Total) {
				return false, latest, nil
			}
			continue
		}
		var bare []scheduleRunBrief
		if err := json.Unmarshal(body, &bare); err != nil {
			return false, latest, fmt.Errorf("decode schedule runs: %w", err)
		}
		if latest == nil && len(bare) > 0 {
			v := bare[0]
			latest = &v
		}
		for _, run := range bare {
			if run.Status == "succeeded" {
				return true, latest, nil
			}
		}
		return false, latest, nil
	}
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

// verifyEnabledScheduleFreshness implements the opt-in broader schedule gate:
// every enabled schedule must satisfy the server-computed stale predicate. It
// deliberately consumes the API's answer instead of duplicating cron,
// timezone, timeout, and grace arithmetic in the CLI.
func verifyEnabledScheduleFreshness(cfg *cliConfig, slug string, res *applyResult) error {
	schedules, err := listSchedules(cfg, slug)
	if err != nil {
		res.failureKind = failureScheduleStateMissing
		return fmt.Errorf("verify schedule freshness: %w", err)
	}
	var failures []string
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		if schedule.Stale == nil {
			res.freshnessGate = append(res.freshnessGate, scheduleGateOutcome{Schedule: schedule.Name, State: "unavailable"})
			res.failureKind = failureScheduleStateMissing
			return fmt.Errorf("verify schedule freshness: server did not report stale state for schedule %q; upgrade the server or omit --verify-schedules", schedule.Name)
		}
		if !*schedule.Stale {
			continue
		}

		outcome := scheduleGateOutcome{Schedule: schedule.Name, State: "stale"}
		if schedule.Refreshing != nil && *schedule.Refreshing {
			outcome.State = "stale_refreshing"
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
		failures = append(failures, describeStaleSchedule(outcome, time.Now()))
	}
	if len(failures) == 0 {
		return nil
	}
	res.failureKind = failureScheduleStale
	return fmt.Errorf("schedule freshness gate unsatisfied: %s", strings.Join(failures, "; "))
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

// restartAppAfterWarm cycles a serving app after every first-fire completed so
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
	fmt.Fprintf(out, "%s: restarted after first-fire warm-up\n", slug)
	return true, nil
}
