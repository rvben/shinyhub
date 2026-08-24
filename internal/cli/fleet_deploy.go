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

	"github.com/rvben/shinyhub/internal/deployfail"
)

// fleetHealthTimeout bounds the post-deploy health wait. First-run uv syncs
// can take minutes, so this is generous relative to the 60s interactive
// `deploy --wait` default. It is also the fallback when --health-timeout is
// unset or non-positive, so the flag can extend the wait but never disable it.
const fleetHealthTimeout = 120 * time.Second

// fleetWarmTimeout is deliberately separate from readiness: data refreshes
// commonly take much longer than starting the serving process. The deadline is
// shared by every first-fire for one app rather than renewed per schedule.
const fleetWarmTimeout = 15 * time.Minute

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

func warmTimeoutDuration(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return fleetWarmTimeout
	}
	return timeout
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
func deployAppBundle(cfg *cliConfig, slug, dir, visibility, project string, out io.Writer, runID string, timeout time.Duration) (promoted string, committed bool, firstFires []firstFireRef, kind deployfail.Kind, err error) {
	return deployAppBundleFromSpec(cfg, slug, bundleBuildSpec{Dir: dir}, visibility, project, out, runID, timeout)
}

func deployAppBundleFromSpec(cfg *cliConfig, slug string, spec bundleBuildSpec, visibility, project string, out io.Writer, runID string, timeout time.Duration) (promoted string, committed bool, firstFires []firstFireRef, kind deployfail.Kind, err error) {
	if err := ensureFleetAppWithRun(cfg, slug, visibility, project, out, runID); err != nil {
		return "", false, nil, deployfail.Unknown, err
	}
	buf, summary, err := zipDirFromSpec(spec)
	if err != nil {
		return "", false, nil, deployfail.ZipError, fmt.Errorf("bundle %s: %w", slug, err)
	}
	if summary != "" {
		fmt.Fprintf(out, "  %s: %s\n", slug, summary)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return "", false, nil, deployfail.ZipError, err
	}
	if _, err := io.Copy(part, buf); err != nil {
		return "", false, nil, deployfail.ZipError, err
	}
	if err := writer.Close(); err != nil {
		return "", false, nil, deployfail.ZipError, err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		// A malformed URL/method, not a packaging failure.
		return "", false, nil, deployfail.Unknown, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	decorateFleetRequest(req, runID)
	// Deploy can take several minutes on first run (uv downloads packages).
	// Use the untimed client to match the SSE logs command.
	resp, err := streamClient.Do(req)
	if err != nil {
		return "", false, nil, deployfail.TransportError, fmt.Errorf("deploy %s: %w", slug, err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", false, nil, failureKindFromBody(resp.StatusCode, rb), &httpStatusError{
			Status: resp.StatusCode,
			msg:    fmt.Sprintf("deploy %s failed: HTTP %d: %s", slug, resp.StatusCode, string(rb)),
		}
	}

	firstFires = firstFireRefsFromDeployResponse(rb)

	// Surface the same post-deploy-hooks-skipped warning the single-app deploy
	// prints, so a fleet operator is not left unaware that setup hooks did not
	// run under the container runtime.
	var deployResp map[string]any
	var keptStopped bool
	if err := json.Unmarshal(rb, &deployResp); err == nil {
		keptStopped, _ = deployResp["kept_stopped"].(bool)
		if summary := formatHookExecutionSummary(deployResp); summary != "" {
			fmt.Fprintf(out, "  %s: %s\n", slug, summary)
		}
		if warn := formatHooksSkippedWarning(deployResp["hooks_skipped"]); warn != "" {
			fmt.Fprintf(out, "  %s: %s\n", slug, warn)
		}
		// Same reasoning as the hooks-skipped warning above: a fleet operator
		// gets the same shadowed-upload notice the single-app `deploy` prints,
		// since they are the most likely person to have uploaded the image a
		// manifest icon now shadows.
		if warn := formatIconShadowWarning(deployResp["manifest"]); warn != "" {
			fmt.Fprintf(out, "  %s: %s\n", slug, warn)
		}
		// Server advisories about declared-but-inert settings (for example a
		// keep-warm floor under elastic isolation). Printed before the health
		// wait so the operator reads why an app will sit at idle instead of
		// discovering it from the wait itself.
		for _, warn := range formatManifestWarnings(deployResp["manifest"]) {
			fmt.Fprintf(out, "  %s: %s\n", slug, warn)
		}
	}

	// Bundle accepted: from here on the deploy is committed even if a
	// post-deploy step fails. A failure past this point is not the deploy's own
	// cause (the server already returned 2xx), so it is reported as Unknown.
	//
	// An app the operator stopped is left stopped by the deploy, so it will
	// never report healthy. Polling it could only run to the deadline and then
	// fail - and be retried - a deploy the server already accepted, so the wait
	// is skipped and the state is reported instead.
	if keptStopped {
		fmt.Fprintf(out, "  %s: stopped, so the new version is not serving yet; start it with `shinyhub apps start %s`\n", slug, slug)
	} else if err := waitForFleetHealthy(cfg, slug, out, timeout); err != nil {
		return "", true, firstFires, deployfail.Unknown, err
	}
	promoted, derr := readPromotedDigest(cfg, slug)
	if derr != nil {
		return promoted, true, firstFires, deployfail.Unknown, derr
	}
	return promoted, true, firstFires, "", nil
}

// failureKindFromBody extracts the failure_kind a deploy failure advertises. A
// new server emits {"error","failure_kind"}; an old server emits {"error"} only.
// An explicit, valid failure_kind always wins. Otherwise the status decides: a
// 4xx is a client-side rejection the server refused before running the deploy,
// so the 5xx-oriented text classifier (which defaults to server_error) must not
// run on it - bundle/request-content rejections map to bundle_invalid, other 4xx
// (auth, rate-limit, not-found) are unclassifiable. A 5xx falls back to text
// classification (partial recovery: build_failed and bundle_invalid survive in
// the default-branch text, reworded runtime/health messages fall to server_error).
func failureKindFromBody(status int, body []byte) deployfail.Kind {
	var env struct {
		Error       string `json:"error"`
		FailureKind string `json:"failure_kind"`
	}
	_ = json.Unmarshal(body, &env)
	if k := deployfail.Kind(env.FailureKind); k.Valid() {
		return k
	}
	switch {
	case status == http.StatusBadRequest,
		status == http.StatusRequestEntityTooLarge,
		status == http.StatusUnprocessableEntity:
		return deployfail.BundleInvalid
	case status >= 400 && status < 500:
		return deployfail.Unknown
	}
	msg := env.Error
	if msg == "" {
		msg = string(body)
	}
	return deployfail.ClassifyMessage(msg)
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
// absent; visibility is forwarded as the access value, and project as
// project_slug.
func ensureFleetApp(cfg *cliConfig, slug, visibility, project string, out io.Writer) error {
	return ensureAppCore(cfg, slug, visibility, project, out, false)
}

func ensureFleetAppWithRun(cfg *cliConfig, slug, visibility, project string, out io.Writer, runID string) error {
	return ensureAppCore(cfg, slug, visibility, project, out, false, runID)
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
	s := stylerFor(out)
	start := now()
	deadline := start.Add(timeout)
	lastProgress := start
	var lastErr error
	var lastStatus string
	unknownReported := false
	for {
		t := now()
		ready, status, err := poll()
		if err == nil && ready {
			fmt.Fprintf(out, "  %s: %s after %s\n",
				slug, s.status("healthy"), s.dim(t.Sub(start).Round(time.Second).String()))
			return nil
		}
		if err != nil {
			lastErr = err
			var he *deployHTTPError
			if errors.As(err, &he) && he.fatal() {
				return fmt.Errorf("checking %s: %w", slug, err)
			}
		} else {
			lastStatus = status
			// A status this CLI cannot classify is reported on first sighting,
			// not on the progress cadence: it is the one thing the operator
			// can act on before the timeout burns.
			if hint := unknownStatusHint(status); hint != "" && !unknownReported {
				unknownReported = true
				fmt.Fprintf(out, "  %s: %s\n", slug, hint)
			}
		}
		if isTerminalStatus(status) {
			return fmt.Errorf("%s %s during startup; run: shinyhub apps logs %s", slug, status, slug)
		}
		if !t.Before(deadline) {
			break
		}
		if t.Sub(lastProgress) >= progressEvery {
			fmt.Fprintf(out, "  %s: still %s %s\n", slug, s.status(waitingStatusWord(lastStatus)),
				s.dim(fmt.Sprintf("(%s/%s)", t.Sub(start).Round(time.Second), timeout)))
			lastProgress = t
		}
		sleep(pollEvery)
	}
	// The timeout names what the server last said, so a 15-minute wait ends
	// in a diagnosis rather than a bare "timed out".
	detail := ""
	if hint := unknownStatusHint(lastStatus); hint != "" {
		detail = hint
	} else if lastStatus != "" {
		detail = "last status: " + lastStatus
	}
	if lastErr != nil {
		if detail != "" {
			detail += "; "
		}
		detail += fmt.Sprintf("last error: %v", lastErr)
	}
	if detail != "" {
		return fmt.Errorf("timed out after %s waiting for %s to be healthy (%s)", timeout, slug, detail)
	}
	return fmt.Errorf("timed out after %s waiting for %s to be healthy", timeout, slug)
}
