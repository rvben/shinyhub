package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	failureScheduleRefreshFailed      = "schedule_refresh_failed"
	failureScheduleRefreshTimeout     = "schedule_refresh_timeout"
	failureScheduleRefreshUnavailable = "schedule_refresh_unavailable"
)

// scheduleRefreshOutcome records recovery separately from deploy obligations.
// Disposition identifies whether this apply admitted the run or joined it.
type scheduleRefreshOutcome struct {
	Schedule    string `json:"schedule"`
	RunID       int64  `json:"run_id,omitempty"`
	Disposition string `json:"disposition"`
	Status      string `json:"status,omitempty"`
}

type scheduleRefreshAdmission struct {
	ScheduleID int64  `json:"schedule_id"`
	Schedule   string `json:"schedule"`
	Status     string `json:"status"`
	RunID      int64  `json:"run_id,omitempty"`
}

// requestStaleScheduleRefresh never retries. A transport failure or malformed
// successful response may follow admission and therefore has unknown effects.
func requestStaleScheduleRefresh(ctx context.Context, cfg *cliConfig, slug string, schedule scheduleDTO) (scheduleRefreshAdmission, bool, error) {
	var admission scheduleRefreshAdmission
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/refresh-stale", cfg.Host, slug, schedule.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return admission, false, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return admission, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return admission, resp.StatusCode >= 500, &deployHTTPError{statusCode: resp.StatusCode, body: string(body)}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&admission); err != nil {
		return admission, true, fmt.Errorf("invalid refresh admission response: %w", err)
	}
	if admission.ScheduleID != schedule.ID || admission.Schedule != schedule.Name {
		return admission, true, errors.New("refresh admission returned a different schedule identity")
	}
	switch admission.Status {
	case "started", "joined":
		if admission.RunID <= 0 {
			return admission, true, errors.New("refresh admission omitted the exact run ID")
		}
	case "fresh", "disabled":
		if admission.RunID != 0 {
			return admission, true, errors.New("no-op refresh admission unexpectedly returned a run ID")
		}
	default:
		return admission, true, fmt.Errorf("unknown refresh admission status %q", admission.Status)
	}
	return admission, false, nil
}

func refreshStaleSchedules(cfg *cliConfig, slug string, res *applyResult, timeout time.Duration, out io.Writer) error {
	return refreshStaleSchedulesContext(context.Background(), cfg, slug, res, timeout, out)
}

func refreshStaleSchedulesContext(parent context.Context, cfg *cliConfig, slug string, res *applyResult, timeout time.Duration, out io.Writer) error {
	if parent == nil {
		parent = context.Background()
	}
	if res.warmDeadline.IsZero() {
		res.warmDeadline = time.Now().Add(warmTimeoutDuration(timeout))
	}
	ctx, cancel := context.WithDeadline(parent, res.warmDeadline)
	defer cancel()
	fail := func(err error) error {
		if res.failureKind == "" {
			res.failureKind = failureScheduleRefreshUnavailable
			if ctx.Err() != nil || errors.Is(err, errDeployRunTimeout) {
				res.failureKind = failureScheduleRefreshTimeout
			}
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("refresh stale schedules: --warm-timeout exhausted: %w", err))
	}
	schedules, err := listSchedulesContext(ctx, cfg, slug)
	if err != nil {
		return fail(fmt.Errorf("list schedules for refresh: %w", err))
	}
	// Validate the complete candidate set before admitting any producer.
	for _, schedule := range schedules {
		if schedule.Enabled && schedule.Stale == nil {
			return fail(fmt.Errorf("refresh schedule %q: server did not report authoritative freshness", schedule.Name))
		}
	}
	for _, schedule := range schedules {
		if !schedule.Enabled || !*schedule.Stale {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("refresh schedule %q: --warm-timeout exhausted: %w", schedule.Name, err))
		}
		outcome := scheduleRefreshOutcome{Schedule: schedule.Name, Disposition: "unknown"}
		admission, ambiguous, err := requestStaleScheduleRefresh(ctx, cfg, slug, schedule)
		if err != nil {
			if ambiguous {
				res.mutation = mutationUnknown
			}
			res.scheduleRefreshes = append(res.scheduleRefreshes, outcome)
			return fail(fmt.Errorf("refresh schedule %q admission: %w", schedule.Name, err))
		}
		outcome.Disposition = admission.Status
		outcome.RunID = admission.RunID
		if admission.Status == "started" {
			res.mutation = mutationPartial
		}
		res.scheduleRefreshes = append(res.scheduleRefreshes, outcome)
		index := len(res.scheduleRefreshes) - 1
		if admission.RunID == 0 {
			continue
		}
		fmt.Fprintf(out, "  %s: %s refresh run #%d for %s\n", slug, admission.Status, admission.RunID, schedule.Name)
		status, err := waitForDeployRunLoop(func() (string, error) {
			status, err := pollScheduleRunStatusContext(ctx, cfg, slug, schedule.ID, admission.RunID)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return status, errors.Join(errDeployRunTimeout, ctx.Err())
			}
			return status, err
		}, time.Until(res.warmDeadline), time.Second, 10*time.Second, time.Now, time.Sleep, out, slug+"/"+schedule.Name)
		res.scheduleRefreshes[index].Status = status
		if err != nil {
			appendScheduleLogContext(ctx, cfg, slug, schedule.ID, admission.RunID, schedule.Name, res)
			return fail(fmt.Errorf("schedule %q refresh run #%d not confirmed: %w", schedule.Name, admission.RunID, err))
		}
		if status != "succeeded" {
			res.failureKind = failureScheduleRefreshFailed
			appendScheduleLogContext(ctx, cfg, slug, schedule.ID, admission.RunID, schedule.Name, res)
			return fail(fmt.Errorf("schedule %q refresh run #%d %s", schedule.Name, admission.RunID, status))
		}
	}
	return nil
}

// Logs consume only the remaining recovery budget. Even when that budget is
// exhausted, record the run identity so recovery can show the exact log command.
func appendScheduleLogContext(ctx context.Context, cfg *cliConfig, slug string, scheduleID, runID int64, schedule string, res *applyResult) {
	entry := scheduleFailureLog{Schedule: schedule, RunID: runID}
	defer func() { res.scheduleLogs = append(res.scheduleLogs, entry) }()
	if err := ctx.Err(); err != nil {
		entry.FetchError = err.Error()
		return
	}
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d/logs?follow=false", cfg.Host, slug, scheduleID, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		entry.FetchError = err.Error()
		return
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		entry.FetchError = err.Error()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		entry.FetchError = resp.Status
		return
	}
	entry.Tail = parsePlainLines(resp.Body, scheduleLogTailLines)
}
