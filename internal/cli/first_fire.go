package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
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
	LastRunID     int64  `json:"last_run_id,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
}

// scheduleFailureLog identifies the schedule-run log relevant to a warm-gate
// failure. Tail is best-effort; the identity is retained even when fetching
// the log fails so the report can still print the correct command.
type scheduleFailureLog struct {
	Schedule string   `json:"schedule"`
	RunID    int64    `json:"run_id"`
	Tail     []string `json:"tail,omitempty"`
}

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

// firstFireStatusOK reports whether a terminal run status counts as "cache
// warmed" for first-fire purposes. A succeeded run warmed it; a skipped_overlap
// means another run is already warming the schedule (not a failure).
func firstFireStatusOK(status string) bool {
	return status == "succeeded" || status == "skipped_overlap"
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
		t := now()
		status, err := poll()
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
		sleep(pollEvery)
	}
}

// pollScheduleRunStatus fetches GET /api/apps/{slug}/schedules/{id}/runs/{run}
// and returns the run's status string.
func pollScheduleRunStatus(cfg *cliConfig, slug string, scheduleID, runID int64) (string, error) {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d", cfg.Host, slug, scheduleID, runID)
	req, err := http.NewRequest("GET", url, nil)
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

func latestScheduleRun(cfg *cliConfig, slug string, scheduleID int64) (*scheduleRunBrief, error) {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs?limit=1", cfg.Host, slug, scheduleID)
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
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(cfg.Token, "list schedule runs", resp, body)
	}
	var env struct {
		Items []scheduleRunBrief `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Items != nil {
		if len(env.Items) == 0 {
			return nil, nil
		}
		return &env.Items[0], nil
	}
	var bare []scheduleRunBrief
	if err := json.Unmarshal(body, &bare); err != nil {
		return nil, fmt.Errorf("decode schedule runs: %w", err)
	}
	if len(bare) == 0 {
		return nil, nil
	}
	return &bare[0], nil
}

// verifyExistingWarmGate evaluates run_on_register as a convergence level for
// an app whose bundle was not registered during this apply. It performs no
// remote work when the local manifest has no enabled run_on_register schedule,
// and never triggers a schedule. A prior success closes the gate; otherwise the
// app remains failed until an operator fixes the job and a run succeeds.
func verifyExistingWarmGate(cfg *cliConfig, slug, bundleDir string, res *applyResult) error {
	m, err := deploy.LoadManifest(bundleDir)
	if err != nil {
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
		return fmt.Errorf("verify run_on_register schedules: %w", err)
	}
	byName := make(map[string]scheduleDTO, len(remote))
	for _, schedule := range remote {
		byName[schedule.Name] = schedule
	}

	var failures []string
	for _, name := range declared {
		schedule, ok := byName[name]
		if !ok {
			res.warmGate = append(res.warmGate, scheduleGateOutcome{Schedule: name, State: "missing"})
			failures = append(failures, fmt.Sprintf("schedule %q is missing", name))
			continue
		}
		if schedule.LastSuccessAt != nil && *schedule.LastSuccessAt != "" {
			continue
		}

		outcome := scheduleGateOutcome{Schedule: name, State: "never_succeeded"}
		if schedule.LastRunStatus != nil {
			outcome.LastRunStatus = *schedule.LastRunStatus
		}
		if schedule.LastRunAt != nil {
			outcome.LastRunAt = *schedule.LastRunAt
		}
		latest, lerr := latestScheduleRun(cfg, slug, schedule.ID)
		if lerr != nil {
			return fmt.Errorf("verify schedule %q run history: %w", name, lerr)
		}
		// A server predating last_success_at may still expose run history. The
		// newest succeeded run is sufficient to close the gate without treating
		// an absent freshness field as a never-succeeded claim.
		if latest != nil && latest.Status == "succeeded" {
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
			tail, _ := fetchScheduleLogTail(cfg, slug, schedule.ID, latest.ID, scheduleLogTailLines)
			res.scheduleLogs = append(res.scheduleLogs, scheduleFailureLog{Schedule: name, RunID: latest.ID, Tail: tail})
		}
		res.warmGate = append(res.warmGate, outcome)
		failures = append(failures, describeNeverSucceeded(outcome, time.Now()))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("warm gate already unsatisfied before this apply: %s", strings.Join(failures, "; "))
}

func describeNeverSucceeded(outcome scheduleGateOutcome, now time.Time) string {
	detail := fmt.Sprintf("schedule %q has never succeeded", outcome.Schedule)
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

// verifyEnabledScheduleFreshness implements the opt-in broader schedule gate:
// every enabled schedule must satisfy the server-computed stale predicate. It
// deliberately consumes the API's answer instead of duplicating cron,
// timezone, timeout, and grace arithmetic in the CLI.
func verifyEnabledScheduleFreshness(cfg *cliConfig, slug string, res *applyResult) error {
	schedules, err := listSchedules(cfg, slug)
	if err != nil {
		return fmt.Errorf("verify schedule freshness: %w", err)
	}
	var failures []string
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		if schedule.Stale == nil {
			res.freshnessGate = append(res.freshnessGate, scheduleGateOutcome{Schedule: schedule.Name, State: "unavailable"})
			return fmt.Errorf("verify schedule freshness: server did not report stale state for schedule %q; upgrade the server or omit --verify-schedules", schedule.Name)
		}
		if !*schedule.Stale {
			continue
		}

		outcome := scheduleGateOutcome{Schedule: schedule.Name, State: "stale"}
		if schedule.LastRunStatus != nil {
			outcome.LastRunStatus = *schedule.LastRunStatus
		}
		if schedule.LastRunAt != nil {
			outcome.LastRunAt = *schedule.LastRunAt
		}
		latest, lerr := latestScheduleRun(cfg, slug, schedule.ID)
		if lerr != nil {
			return fmt.Errorf("verify schedule %q run history: %w", schedule.Name, lerr)
		}
		if latest != nil {
			outcome.LastRunID = latest.ID
			if outcome.LastRunStatus == "" {
				outcome.LastRunStatus = latest.Status
			}
			if outcome.LastRunAt == "" {
				outcome.LastRunAt = latest.StartedAt
			}
			tail, _ := fetchScheduleLogTail(cfg, slug, schedule.ID, latest.ID, scheduleLogTailLines)
			res.scheduleLogs = append(res.scheduleLogs, scheduleFailureLog{Schedule: schedule.Name, RunID: latest.ID, Tail: tail})
		}
		res.freshnessGate = append(res.freshnessGate, outcome)
		failures = append(failures, describeStaleSchedule(outcome, time.Now()))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("schedule freshness gate unsatisfied: %s", strings.Join(failures, "; "))
}

func describeStaleSchedule(outcome scheduleGateOutcome, now time.Time) string {
	detail := fmt.Sprintf("schedule %q is stale", outcome.Schedule)
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
