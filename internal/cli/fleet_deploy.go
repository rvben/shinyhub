package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// fleetHealthTimeout bounds the post-deploy health wait. First-run uv syncs
// can take minutes, so this is generous relative to the 60s interactive
// `deploy --wait` default. It is also the fallback when --health-timeout is
// unset or non-positive, so the flag can extend the wait but never disable it.
const fleetHealthTimeout = 120 * time.Second

// fleetHealthProgressInterval is how often the health wait emits a progress
// line so a long first-run sync looks like progress, not a hang.
const fleetHealthProgressInterval = 15 * time.Second

// healthTimeoutDuration converts the --health-timeout flag (seconds) to a
// duration, falling back to the generous fleet default when the value is
// non-positive so the flag cannot accidentally disable the health wait.
func healthTimeoutDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return fleetHealthTimeout
	}
	return time.Duration(seconds) * time.Second
}

// deployAppBundle deploys one app's local directory through the existing
// per-app deploy mechanism (ensure app exists with the requested visibility,
// bundle, upload, wait for health), then re-reads the app from the server
// and returns its freshly promoted content_digest. Returning the post-deploy
// digest lets a same-run config PATCH carry a precondition built from the
// deployment this run just performed (otherwise it would 409 against us).
//
// committed reports whether the server accepted the bundle: it is true only
// when POST /api/apps/{slug}/deploy returned 2xx, in which case the bundle is
// live even if a later step (health wait / digest readback) then fails. A
// non-2xx response is reported committed=false because the deploy endpoint
// returns 500 both BEFORE promotion (BeginDeployment, quota, deploy.Run then
// restore) and AFTER it (PromoteDeployment record failure, manifest schedule
// apply), so the status alone cannot tell whether the new bundle went live -
// callers that care (adopt) resolve that authoritatively with a digest
// readback.
func deployAppBundle(cfg *cliConfig, slug, dir, visibility string, out io.Writer, runID string, timeout time.Duration) (promoted string, committed bool, err error) {
	if err := ensureFleetApp(cfg, slug, visibility, out); err != nil {
		return "", false, err
	}
	buf, summary, err := zipDir(dir)
	if err != nil {
		return "", false, fmt.Errorf("bundle %s: %w", slug, err)
	}
	if summary != "" {
		fmt.Fprintf(out, "  %s: %s\n", slug, summary)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return "", false, err
	}
	if _, err := io.Copy(part, buf); err != nil {
		return "", false, err
	}
	if err := writer.Close(); err != nil {
		return "", false, err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	decorateFleetRequest(req, runID)
	// Deploy can take several minutes on first run (uv downloads packages).
	// Use http.DefaultClient (no timeout) to match the SSE logs command.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("deploy %s: %w", slug, err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("deploy %s failed: HTTP %d: %s", slug, resp.StatusCode, string(rb))
	}

	// Surface the same post-deploy-hooks-skipped warning the single-app deploy
	// prints, so a fleet operator is not left unaware that setup hooks did not
	// run under the container runtime.
	var deployResp map[string]any
	if err := json.Unmarshal(rb, &deployResp); err == nil {
		if warn := formatHooksSkippedWarning(deployResp["hooks_skipped"]); warn != "" {
			fmt.Fprintf(out, "  %s: %s\n", slug, warn)
		}
	}

	// Bundle accepted: from here on the deploy is committed even if a
	// post-deploy step fails.
	if err := waitForFleetHealthy(cfg, slug, out, timeout); err != nil {
		return "", true, err
	}
	promoted, err = readPromotedDigest(cfg, slug)
	return promoted, true, err
}

// readPromotedDigest re-GETs the app list and returns the live (succeeded)
// content_digest for slug, or "" if the server does not expose one.
func readPromotedDigest(cfg *cliConfig, slug string) (string, error) {
	apps, err := fetchApps(cfg)
	if err != nil {
		return "", fmt.Errorf("read back digest for %s: %w", slug, err)
	}
	for _, a := range apps {
		if a.Slug == slug {
			return a.ContentDigest, nil
		}
	}
	return "", nil
}

// ensureFleetApp ensures the app exists with the requested visibility,
// delegating to the existing per-app create/verify helper. That helper issues
// GET /api/apps/{slug} then POST /api/apps with {"slug","name","access"} when
// absent; visibility is forwarded as the access value.
func ensureFleetApp(cfg *cliConfig, slug, visibility string, out io.Writer) error {
	return ensureAppCore(cfg, slug, visibility, out, false)
}

// waitForFleetHealthy blocks until the app reports running or a terminal
// failure, emitting periodic progress lines to out. On failure it appends the
// app's recent log tail so the operator has something actionable.
func waitForFleetHealthy(cfg *cliConfig, slug string, out io.Writer, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = fleetHealthTimeout
	}
	poll := func() (bool, string, error) { return pollAppStatus(cfg, slug) }
	err := waitForFleetHealthLoop(slug, timeout, 2*time.Second, fleetHealthProgressInterval,
		poll, time.Now, time.Sleep, out)
	if err != nil {
		printLogTail(cfg, slug, out)
	}
	return err
}

// waitForFleetHealthLoop blocks until poll reports ready, a terminal startup
// failure, or timeout elapses. Every progressEvery it writes a one-line update
// (app, elapsed/timeout) to out so a long first-run uv sync reads as progress
// rather than a hang. A fatal poll error (auth / gone) aborts immediately;
// transient 5xx and transport errors keep the loop going until the deadline.
// now and sleep are injected so the cadence is deterministic in tests.
func waitForFleetHealthLoop(slug string, timeout, pollEvery, progressEvery time.Duration,
	poll func() (bool, string, error), now func() time.Time, sleep func(time.Duration), out io.Writer) error {
	start := now()
	deadline := start.Add(timeout)
	lastProgress := start
	var lastErr error
	for {
		t := now()
		ready, status, err := poll()
		if err == nil && ready {
			fmt.Fprintf(out, "  %s: healthy after %s\n", slug, t.Sub(start).Round(time.Second))
			return nil
		}
		if err != nil {
			lastErr = err
			var he *httpStatusError
			if errors.As(err, &he) && he.fatal() {
				return fmt.Errorf("checking %s: %w", slug, err)
			}
		}
		if isTerminalStatus(status) {
			return fmt.Errorf("%s %s during startup; run: shinyhub apps logs %s", slug, status, slug)
		}
		if !t.Before(deadline) {
			break
		}
		if t.Sub(lastProgress) >= progressEvery {
			fmt.Fprintf(out, "  %s: still starting (%s/%s)\n",
				slug, t.Sub(start).Round(time.Second), timeout)
			lastProgress = t
		}
		sleep(pollEvery)
	}
	if lastErr != nil {
		return fmt.Errorf("timed out after %s waiting for %s to be healthy (last error: %v)", timeout, slug, lastErr)
	}
	return fmt.Errorf("timed out after %s waiting for %s to be healthy", timeout, slug)
}
