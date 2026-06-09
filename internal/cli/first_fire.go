package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// firstFireRef identifies a run_on_register first-fire dispatched by the server
// during a deploy, parsed from the deploy response's manifest.schedules[].
type firstFireRef struct {
	Schedule   string
	ScheduleID int64
	RunID      int64
}

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
			var he *httpStatusError
			if errors.As(err, &he) && he.fatal() {
				return lastStatus, err
			}
			// transient (5xx / transport): keep looping until the deadline
		}
		if !t.Before(deadline) {
			return lastStatus, errFirstFireTimeout
		}
		if t.Sub(lastProgress) >= progressEvery {
			fmt.Fprintf(out, "  %s: first-fire still running (%s/%s)\n",
				label, t.Sub(start).Round(time.Second), timeout)
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
		return "", &httpStatusError{statusCode: resp.StatusCode, body: string(body)}
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return "", err
	}
	return run.Status, nil
}
