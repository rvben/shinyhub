package cli

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deployevent"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

var slugInvalidRE = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeSlug lowercases the name, replaces runs of non-alphanumeric characters
// with a single dash, and produces a result that satisfies the canonical slug
// rule (see internal/slug). Truncation happens before the trailing-dash trim
// because cutting a 64th-position dash off mid-string would otherwise leave a
// slug ending in `-`, which slugpkg.Valid rejects.
func sanitizeSlug(name string) string {
	s := strings.ToLower(name)
	s = slugInvalidRE.ReplaceAllString(s, "-")
	if len(s) > slugpkg.MaxLen {
		s = s[:slugpkg.MaxLen]
	}
	s = strings.Trim(s, "-")
	return s
}

// deployFlags holds the parsed flags for a single `deploy` invocation. It is
// constructed fresh per command instance (no package-level state) so repeated
// or shuffled test runs cannot leak flag values between each other.
type deployFlags struct {
	slug             string
	wait             bool
	waitForWarm      bool
	restartAfterWarm bool
	waitTimeout      int    // seconds
	git              string // git repo URL; if set, clone instead of using local dir
	branch           string // branch/tag to check out (default: default branch)
	subdir           string // subdirectory within the repo containing the app
	visibility       string // app access level: private, shared, public (empty = use server default)
	start            bool   // start the app even if it was stopped before this deploy
	open             bool   // start, wait for health, verify the route, and open it

	waitForServer time.Duration // poll /api/server-info until the server is ready before deploying
}

// newDeployCmd builds a fresh deploy command each time it is called, with its
// flags bound to a per-instance deployFlags value.
func newDeployCmd() *cobra.Command {
	f := &deployFlags{}
	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Deploy a Shiny app to ShinyHub",
		Long: `Deploy a Shiny app bundle to ShinyHub.

Bundle: the given directory is zipped and uploaded. Pass '.' to deploy the
current directory, or a path like './app'. The bundle must contain an entry
point at its root: Python apps need app.py plus requirements.txt (or
pyproject.toml); R apps need app.R (plus renv.lock for pinned deps). An
[app] command in shinyhub.toml overrides this detection. Data and cache
directories are excluded automatically; add a .shinyhubignore (or .gitignore)
to exclude more. Validate the optional manifest first with
'shinyhub manifest validate'.

Server: connect once with 'shinyhub connect https://hub.example.com'. The saved
current server is used by default; the global --host flag targets a different
saved name or URL for one command. CI can set SHINYHUB_HOST and SHINYHUB_TOKEN.

Manifest: if the bundle contains a shinyhub.toml at its root, ShinyHub applies
it on deploy - [app] scaling/hibernate overrides, [[hook]] post-deploy commands,
and [[schedule]] cron jobs. The manifest is optional. Set [app]
startup_timeout_seconds (1-3600, default 120) to lengthen the readiness deadline
for an app that warms up slowly at import; it travels with the bundle and also
applies on wake, scale, and rollback.

Stopped apps: an app that was stopped before the deploy stays stopped. The
bundle is still built, validated and recorded, so 'shinyhub apps start <slug>'
brings up the new version. Pass --start to deploy and start in one step. Pass
--open for the complete interactive flow: start, wait for health, verify the
user-facing route when it is public, and open it in your default browser.

Slug and URL: the app is served at <host>/app/<slug>/. The slug defaults to the
directory name (sanitized); override it with --slug. Slug rule: lowercase
letters, digits, and single hyphens; it must not start or end with a hyphen.

Deploying behind an auth proxy: when ShinyHub sits behind an auth proxy
(Authelia, oauth2-proxy, Cloudflare Access, etc.) the CLI cannot complete the
browser redirect that the proxy requires, so interactive 'shinyhub login' does
not work from a CI runner. Instead, deploy directly to the app port using
SHINYHUB_HOST set to the internal address and SHINYHUB_TOKEN set to a
pre-shared deploy token (the SHINYHUB_DEPLOY_TOKEN value configured on the
server). See docs/reverse-proxy/deploying-behind-a-proxy.md for the full setup.

Progress: interactive deploys show each server-side phase and its elapsed time.
Use --output json for one final result document, or --output ndjson for a stable
event stream suitable for CI logs and automation. New CLIs automatically fall
back to the legacy response when deploying to an older server.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.slug, "slug", "", "App slug; serves at /app/<slug>/ (lowercase letters, digits, single hyphens; no leading/trailing hyphen). Defaults to the directory name")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "Wait until deployment is healthy")
	cmd.Flags().IntVar(&f.waitTimeout, "wait-timeout", 300, "Seconds to wait for healthy status when --wait is set (first-run dependency installs can take minutes)")
	cmd.Flags().BoolVar(&f.waitForWarm, "wait-for-warm", false, "Wait for any run_on_register first-fire to finish (uses --wait-timeout); a genuine failure exits non-zero")
	cmd.Flags().BoolVar(&f.restartAfterWarm, "restart-after-warm", false, "Wait for run_on_register first-fires, then restart serving replicas so startup-loaded data is refreshed")
	cmd.Flags().StringVar(&f.git, "git", "", "Git repository URL to clone and deploy")
	cmd.Flags().StringVar(&f.branch, "branch", "", "Branch or tag to deploy (default: repo default)")
	cmd.Flags().StringVar(&f.subdir, "subdir", "", "Subdirectory within repo containing the app")
	cmd.Flags().StringVar(&f.visibility, "visibility", "", "App visibility for new apps: private (members only), shared (every signed-in user; alias: internal), or public (anyone, no sign-in). Default: server config")
	cmd.Flags().DurationVar(&f.waitForServer, "wait-for-server", 0, "Poll /api/server-info until the server is ready (e.g. 2m) before deploying")
	cmd.Flags().BoolVar(&f.start, "start", false, "Start the app after deploying even if it was stopped; without this a stopped app stays stopped")
	cmd.Flags().BoolVar(&f.open, "open", false, "Start the app, wait until it is healthy, verify its public route, and open it in the default browser")
	return cmd
}

func runDeploy(cmd *cobra.Command, args []string, f *deployFlags) error {
	// Opening a stopped or still-starting app is a broken promise. Treat --open
	// as the complete success-to-use workflow, including the two prerequisites
	// a person would otherwise have to discover and spell out themselves.
	if f.open {
		f.start = true
		f.wait = true
	}
	if f.restartAfterWarm {
		f.waitForWarm = true
	}
	source, err := resolveDeploymentSource(args, f)
	if err != nil {
		return err
	}
	defer source.cleanup()
	abs, slug := source.Dir, source.Slug
	f.visibility = source.Visibility

	cfg, err := loadConfig()
	if err != nil {
		connected, connectErr := offerConnectForFirstDeploy(cmd)
		if connectErr != nil {
			return connectErr
		}
		if !connected {
			return err
		}
		cfg, err = loadConfig()
		if err != nil {
			return err
		}
	}

	// When the target host may still be coming up (e.g. a freshly recycled EC2
	// where a front proxy answers before shinyhub is installed), block until
	// /api/server-info reports a healthy shinyhub instead of failing with a
	// misleading auth error. Exit code 6 distinguishes "server not ready" from
	// a real transport/auth failure.
	if f.waitForServer > 0 {
		if _, werr := waitForServerReady(cfg, f.waitForServer, serverPollInterval, cmd.ErrOrStderr(), time.Now, time.Sleep); werr != nil {
			return &ExitCodeError{Code: 6, Err: werr}
		}
	}

	format, err := resolveDeployFormat()
	if err != nil {
		return err
	}
	errW := cmd.ErrOrStderr()
	stdOut := cmd.OutOrStdout()

	if !quietFlag && format != formatNDJSON {
		fmt.Fprintf(errW, "Bundling %s...\n", abs)
	}
	bundlePlan, _, err := prepareDeployment(abs)
	if err != nil {
		if format == formatNDJSON {
			e := deployevent.Event{Type: deployevent.TypeError, Phase: "bundle", Message: err.Error(), FailureKind: "unknown"}
			if writeErr := writeDeployEvent(stdOut, e); writeErr != nil {
				return writeErr
			}
			return &ExitCodeError{Code: 1, Err: err, Reported: true}
		}
		return err
	}
	bundleBuf := bundlePlan.Buffer
	bundleEvent := deployevent.Phase("bundle", deployevent.StatusCompleted, "Bundle ready")
	bundleEvent.FileCount = bundlePlan.FileCount
	bundleEvent.Bytes = int64(bundlePlan.CompressedBytes)
	bundleEvent.Digest = bundlePlan.Digest
	if format == formatNDJSON {
		if err := writeDeployEvent(stdOut, bundleEvent); err != nil {
			return err
		}
	} else if !quietFlag {
		fmt.Fprintf(errW, "  ✓ Bundle ready: %d files, %s upload, %s\n",
			bundlePlan.FileCount, humanBytes(int64(bundlePlan.CompressedBytes)), bundlePlan.Digest)
	}
	summary := summarizeDeploymentRejections(bundlePlan.ProtectedPaths)
	if summary != "" {
		fmt.Fprintln(errW, summary)
	}

	// Best-effort pre-flight: an R bundle on a server with no R runtime will
	// fail server-side. Warn early so the developer knows it is a host setup
	// issue, not their bundle. Silent when the server is older or unreachable.
	if looksLikeRApp(abs) {
		if available, known := serverRuntimeAvailable(cfg, "r"); known && !available {
			fmt.Fprintln(errW, "Warning: this looks like an R app, but the server reports no R runtime (Rscript). "+
				"The deploy will likely fail - ask your administrator to install R or use a container runtime.")
		}
	}

	if err := ensureApp(cfg, slug, f.visibility); err != nil {
		return err
	}

	// The deploy request is the longest blocking step (a first deploy installs
	// dependencies server-side), so on a terminal it animates with its elapsed
	// time rather than sitting on one static line for minutes.
	deployStarted := time.Now()
	var deploying *progress
	if format != formatNDJSON && !quietFlag {
		deploying = newProgress(errW, fmt.Sprintf("Uploading and deploying %s to %s...", slug, cfg.Host))
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return fmt.Errorf("build upload: %w", err)
	}
	if _, err := io.Copy(part, bundleBuf); err != nil {
		return fmt.Errorf("build upload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("build upload: %w", err)
	}
	uploadBytes := int64(body.Len())

	deployURL := cfg.Host + "/api/apps/" + slug + "/deploy"
	if f.start {
		deployURL += "?start=true"
	}
	req, err := http.NewRequest("POST", deployURL, &body)
	if err != nil {
		if deploying != nil {
			deploying.stop()
		}
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", deployevent.MediaType)

	// Deploy can take several minutes on first run (uv downloads packages).
	// Use the untimed client to match the SSE logs command.
	if format == formatNDJSON {
		upload := deployevent.Phase("upload", deployevent.StatusStarted, "Uploading and validating bundle")
		upload.Bytes = uploadBytes
		if err := writeDeployEvent(stdOut, upload); err != nil {
			return err
		}
	} else if deploying != nil {
		deploying.start()
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		if deploying != nil {
			deploying.stop()
		}
		if format == formatNDJSON {
			e := deployevent.Event{Type: deployevent.TypeError, Phase: "upload", Message: err.Error(), FailureKind: "transport_error"}
			if writeErr := writeDeployEvent(stdOut, e); writeErr != nil {
				return writeErr
			}
			return &ExitCodeError{Code: 3, Err: err, Reported: true}
		}
		// No prefix: the client's message already names the server that could
		// not be reached, and which request it was is not in doubt.
		return err
	}
	defer resp.Body.Close()
	streamed := isDeployEventResponse(resp)
	if format == formatNDJSON && resp.StatusCode < http.StatusBadRequest {
		upload := deployevent.Phase("upload", deployevent.StatusCompleted, "Bundle uploaded and validated")
		upload.Bytes = uploadBytes
		if err := writeDeployEvent(stdOut, upload); err != nil {
			return err
		}
	} else if deploying != nil {
		if streamed {
			deploying.done(" done", "Bundle uploaded and validated")
		} else {
			deploying.stop()
		}
	}

	var out []byte
	if streamed {
		out, err = consumeDeployEvents(resp, format, stdOut, errW, quietFlag)
		if err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.Status >= 500 {
				printLogTail(cfg, slug, errW)
			}
			return err
		}
	} else {
		out, _ = io.ReadAll(resp.Body)
	}
	if resp.StatusCode >= 400 {
		// A startup/deploy failure (HTTP 5xx "deploy failed: ...") is diagnosed
		// fastest from the app's own logs - the health-check message even tells
		// the developer to "check the app logs". Surface the last lines inline so
		// they don't have to run a second `apps logs` command. Other failures
		// (auth, validation) carry no app logs, so gate on the deploy-failure body.
		if resp.StatusCode >= 500 && bytes.Contains(out, []byte("deploy failed")) {
			printLogTail(cfg, slug, cmd.ErrOrStderr())
		}
		httpErr := httpError(cfg.Token, "deploy", resp, out)
		if format == formatNDJSON {
			e := deployevent.Event{Type: deployevent.TypeError, Phase: "upload", Message: httpErr.Error(), StatusCode: resp.StatusCode}
			if err := writeDeployEvent(stdOut, e); err != nil {
				return err
			}
			_, code := statusKind(resp.StatusCode)
			return &ExitCodeError{Code: code, Err: httpErr, Reported: true}
		}
		return httpErr
	}
	if !streamed && format == formatNDJSON {
		if err := writeDeployEvent(stdOut, deployevent.Event{Type: deployevent.TypeResult, Result: json.RawMessage(out)}); err != nil {
			return err
		}
	}

	// Extract fields from the response for the result envelope and human summary.
	var appResp map[string]any
	deployCount := 0
	currentVersion := ""
	keptStopped := false
	deployStatus := ""
	if err := json.Unmarshal(out, &appResp); err == nil {
		if v, ok := appResp["deploy_count"].(float64); ok {
			deployCount = int(v)
		}
		if v, ok := appResp["current_version"].(string); ok {
			currentVersion = v
		}
		keptStopped, _ = appResp["kept_stopped"].(bool)
		deployStatus, _ = appResp["status"].(string)
	}
	if f.open && (keptStopped || deployStatus == "stopped") {
		fmt.Fprintf(errW, "%s remained stopped after deployment; starting it now...\n", slug)
		if _, err := startAppIfNotRunning(cfg, slug); err != nil {
			return &hintedMsgError{
				msg:   fmt.Sprintf("%s deployed successfully, but could not be started for --open: %v", slug, err),
				hint:  fmt.Sprintf("the deployment succeeded; retry with `shinyhub apps start %s`, then `shinyhub apps open %s`", slug, slug),
				cause: err,
			}
		}
		keptStopped = false
	}

	// In JSON mode all prose goes to stderr; one result object on stdout.
	noteW := stdOut
	if format == formatJSON {
		noteW = errW
	} else if format == formatNDJSON {
		noteW = io.Discard
	}
	// How long the deploy took is the question every developer asks next, and on
	// a terminal it is the only place it is recorded. Piped output keeps the
	// exact sentence it has always had, so anything matching on it is unaffected.
	ps := stylerFor(noteW)
	took := ""
	if ps.tty {
		took = " in " + humanElapsed(time.Since(deployStarted))
	}
	deployment := ""
	if deployCount > 0 {
		deployment = fmt.Sprintf(" (deployment #%d)", deployCount)
	}
	target := remoteAppURL(cfg.Host, slug)
	fmt.Fprintf(noteW, "%sDeployed %s%s%s\nURL: %s\n",
		ps.okPrefix(), slug, deployment, took, target)
	if note := formatKeptStoppedNote(keptStopped, slug); note != "" {
		fmt.Fprintln(noteW, note)
	}
	for _, line := range formatManifestSummary(appResp["manifest"]) {
		fmt.Fprintln(noteW, line)
	}
	if summary := formatHookExecutionSummary(appResp); summary != "" {
		fmt.Fprintln(noteW, summary)
	}
	// Surface visibility so the printed URL returning 401 for a brand-new private
	// app is not a confusing surprise. Prose target matches the URL above.
	switch access, _ := appResp["access"].(string); access {
	case "private":
		fmt.Fprintf(noteW, "Access: private (only people you grant can open it) - add someone: shinyhub apps access grant %s <username>\n", slug)
	case "shared", "public":
		fmt.Fprintf(noteW, "Access: %s\n", access)
	}
	if warn := formatHooksSkippedWarning(appResp["hooks_skipped"]); warn != "" && format != formatNDJSON {
		fmt.Fprintln(errW, warn)
	}

	refs := firstFireRefsFromDeployResponse(out)
	warmRestarted := false
	for _, ref := range refs {
		fmt.Fprintf(errW, "%s: first-fire triggered (run #%d)\n", ref.Schedule, ref.RunID)
	}
	if f.waitForWarm && len(refs) > 0 {
		timeout := time.Duration(f.waitTimeout) * time.Second
		var firstFireErr error
		allSucceeded := true
		for _, ref := range refs {
			poll := func() (string, error) { return pollScheduleRunStatus(cfg, slug, ref.ScheduleID, ref.RunID) }
			status, werr := waitForFirstFireLoop(poll, timeout, healthPollInterval, 15*time.Second, time.Now, time.Sleep, errW, ref.Schedule)
			switch {
			case werr != nil:
				allSucceeded = false
				// A timeout or transient poll error is not a hard failure: the run
				// may still be warming and the next deploy self-heals. This matches
				// fleet apply, which also treats an unfinished wait as non-fatal.
				fmt.Fprintf(errW, "%s: first-fire not confirmed: %v (warming may still be in progress)\n", ref.Schedule, werr)
			case status == "skipped_overlap":
				allSucceeded = false
				fmt.Fprintf(errW, "%s: first-fire skipped (another run is warming the cache); warming in progress\n", ref.Schedule)
			case firstFireStatusOK(status):
				fmt.Fprintf(errW, "%s: first-fire %s\n", ref.Schedule, status)
			default:
				allSucceeded = false
				// Dump the failed run's own log so the operator sees why the warm-up
				// failed.
				_ = streamRunLogs(cfg, slug, ref.ScheduleID, ref.RunID, false, cmd)
				firstFireErr = errors.Join(firstFireErr, fmt.Errorf("%s first-fire %s", ref.Schedule, status))
			}
		}
		if firstFireErr != nil {
			return firstFireErr
		}
		if f.restartAfterWarm {
			if !allSucceeded {
				return fmt.Errorf("cannot restart after warm: not every first-fire completed successfully")
			}
			restarted, err := restartAppAfterWarm(cfg, slug, errW)
			if err != nil {
				return err
			}
			warmRestarted = restarted
		}
	}

	// A deploy that deliberately left the app down will never report healthy, so
	// waiting could only end in a timeout that failed a deploy which in fact
	// succeeded. --start is how a caller asks for a running app to wait for.
	if f.wait && !keptStopped {
		if err := waitForHealthyWithOutput(cfg, slug, time.Duration(f.waitTimeout)*time.Second, errW); err != nil {
			return err
		}
	}

	opened := false
	if f.open {
		// Only public routes can be checked anonymously. Private/shared app routes
		// authenticate through a browser cookie and deliberately do not consume
		// the CLI Authorization header, because embedded apps may use that header
		// for their own protocols.
		access, _ := appResp["access"].(string)
		if access == "public" {
			if err := verifyPublicAppRoute(target); err != nil {
				return &hintedMsgError{
					msg:   fmt.Sprintf("%s deployed and became healthy, but its public route check failed: %v", slug, err),
					hint:  fmt.Sprintf("the deployment succeeded; inspect `shinyhub apps logs %s --no-follow`, then retry `shinyhub apps open %s`", slug, slug),
					cause: err,
				}
			}
		}
		opened = openAppURL(target, false, errW)
		if opened {
			fmt.Fprintf(errW, "Opened in your browser: %s\n", target)
		}
	}

	if format == formatJSON {
		// deploy_count and version are always present so consumers can rely on
		// these keys existing. deploy_count is 0 when the server omits it (older
		// servers); version is "" when the server does not report one.
		// kept_stopped is always present for the same reason: a consumer must be
		// able to distinguish "deployed and live" from "deployed and still down"
		// without matching on prose.
		result := map[string]any{
			"status":         "deployed",
			"slug":           slug,
			"deploy_count":   deployCount,
			"version":        currentVersion,
			"kept_stopped":   keptStopped,
			"url":            target,
			"opened":         opened,
			"warm_restarted": warmRestarted,
		}
		for _, key := range []string{"hooks_declared", "hooks_run", "hooks_skipped"} {
			if value, ok := appResp[key]; ok {
				result[key] = value
			}
		}
		if err := json.NewEncoder(stdOut).Encode(result); err != nil {
			return err
		}
	}
	return nil
}

// healthPollInterval is the delay between health polls. It is a package var so
// tests can shorten it; production keeps the 2-second cadence.
var healthPollInterval = 2 * time.Second

// waitForHealthy polls GET /api/apps/{slug} until status is "running" or
// the deadline expires. It writes progress dots to stdout and any failure
// log tail to os.Stderr.
func waitForHealthy(cfg *cliConfig, slug string, timeout time.Duration) error {
	return waitForHealthyWithOutput(cfg, slug, timeout, os.Stderr)
}

// waitForHealthyWithOutput is the testable core of waitForHealthy. It polls
// until the app is running, timed out, or enters a terminal failed state.
// On failure it fetches the last 20 log lines and writes them to errOut,
// followed by a hint pointing to the full logs command.
//
// A 4xx poll response (auth, gone, forbidden) is treated as fatal: continuing
// to poll would only delay the inevitable failure. 5xx and transport errors
// are treated as transient and keep the loop going.
func waitForHealthyWithOutput(cfg *cliConfig, slug string, timeout time.Duration, errOut io.Writer) error {
	deadline := time.Now().Add(timeout)
	p := newProgress(errOut, fmt.Sprintf("Waiting for %s to be healthy", slug))
	var lastErr error
	var lastPollOK bool
	for time.Now().Before(deadline) {
		ready, status, err := pollAppStatus(cfg, slug)
		if err == nil && ready {
			p.done(" ready.", slug+" is healthy")
			return nil
		}
		lastPollOK = err == nil
		if err != nil {
			lastErr = err
			var he *deployHTTPError
			if errors.As(err, &he) && he.fatal() {
				p.stop()
				return fmt.Errorf("checking %s: %w", slug, err)
			}
		}
		if isTerminalStatus(status) {
			p.stop()
			printLogTail(cfg, slug, errOut)
			return fmt.Errorf("%s %s during startup - check logs above or run: shinyhub apps logs %s", slug, status, slug)
		}
		p.step(healthPollInterval)
	}
	p.stop()
	printLogTail(cfg, slug, errOut)
	// If the most recent poll could not reach the app, surface that error: we
	// have no fresh evidence the app is merely still booting, and a persistent
	// transport/5xx failure is the actionable diagnostic. This also covers the
	// case where we never reached the app at all (lastStatus still empty).
	if !lastPollOK && lastErr != nil {
		return fmt.Errorf("timed out after %s waiting for %s to be healthy (last error: %v)", timeout, slug, lastErr)
	}
	// The app was reachable and still in a non-terminal startup state: the
	// deploy was committed and the app is still booting (first-run dependency
	// installs can outlast the wait window). Make clear this is not a failure.
	return fmt.Errorf("deploy committed, but %s is still starting after %s (timed out). "+
		"First-run dependency installs can take longer than this; the app has not failed. "+
		"Check progress with `shinyhub apps logs %s`, or re-run with a larger --wait-timeout", slug, timeout, slug)
}

// isTerminalStatus reports whether an app status indicates a non-recoverable
// failure during startup (as opposed to a transient state like "starting" or
// "stopped", which is a normal intentional stop). Only "crashed" is unambiguously
// a failed-startup state.
func isTerminalStatus(status string) bool {
	return status == "crashed"
}

// logTailLines is the default number of app-log lines surfaced on a deploy
// failure. Shared by the single-app deploy path and fleet apply so both
// entrypoints tail the same amount.
const logTailLines = 20

// fetchLogTail returns the last n non-empty lines of the app's process log via
// GET /api/apps/{slug}/logs?follow=false. It is best-effort: a non-nil error
// (request build, transport, or non-2xx) means the tail is unavailable, and the
// returned slice is nil. Callers that render to a human can surface the error;
// callers that attach the tail to a structured result ignore it.
func fetchLogTail(cfg *cliConfig, slug string, n int) ([]string, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/logs?follow=false", nil)
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

// printLogTail fetches the last logTailLines lines of the app log and writes
// them to w, followed by a hint for the full logs command. On fetch error or
// non-2xx response, a warning line is written to w so the caller always sees
// actionable output even when the log endpoint is unavailable.
func printLogTail(cfg *cliConfig, slug string, w io.Writer) {
	lines, err := fetchLogTail(cfg, slug, logTailLines)
	if err != nil {
		fmt.Fprintf(w, "warning: could not fetch logs: %s\n", err)
		return
	}
	if len(lines) == 0 {
		return
	}
	// Build the whole block and write it once so a syncWriter (parallel fleet
	// apply, where log-tail lines are raw app output and not slug-prefixed) keeps
	// one app's tail intact instead of interleaving it line-by-line with another
	// failing app's tail.
	var b strings.Builder
	b.WriteString("--- last log lines ---\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "--- run `shinyhub apps logs %s` for full logs ---\n", slug)
	_, _ = io.WriteString(w, b.String())
}

// parsePlainLines reads a plain-text response body (one log line per line,
// as returned by GET /api/apps/{slug}/logs?follow=false) and returns the last
// n non-empty lines.
func parsePlainLines(r io.Reader, n int) []string {
	var all []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			all = append(all, line)
		}
	}
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}

// deployHTTPError carries the response status code and body so callers can
// distinguish fatal (4xx) from transient (5xx) HTTP failures while still
// surfacing the server's error envelope to the user.
type deployHTTPError struct {
	statusCode int
	body       string
}

func (e *deployHTTPError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.statusCode, strings.TrimSpace(e.body))
	}
	return fmt.Sprintf("HTTP %d", e.statusCode)
}

// fatal returns true for 4xx codes - auth, not-found, forbidden - which won't
// resolve themselves on retry. 5xx is treated as transient.
func (e *deployHTTPError) fatal() bool {
	return e.statusCode >= 400 && e.statusCode < 500
}

// pollAppStatus issues a single GET /api/apps/{slug} and reports whether the
// app is running and the current status string. It exists as a separate
// function so each iteration's response body is closed before the next poll —
// `defer` inside the loop would keep bodies open for the lifetime of the
// command on long --wait-timeout values.
//
// A non-2xx response is returned as a *deployHTTPError so the caller can
// distinguish "permanent" failures (401/403/404) from transient ones (5xx).
func pollAppStatus(cfg *cliConfig, slug string) (bool, string, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return false, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, "", &deployHTTPError{statusCode: resp.StatusCode, body: string(body)}
	}
	var result struct {
		App struct {
			Status string `json:"status"`
		} `json:"app"`
		// RedeployInFlight is set by the server while an async replica redeploy
		// is cycling the pool. The app row still reports "running" throughout,
		// so this flag is the only honest signal that the new pool is not yet
		// up. Treat the app as not-ready until it clears.
		RedeployInFlight bool `json:"redeploy_in_flight"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "", err
	}
	status := result.App.Status
	return status == "running" && !result.RedeployInFlight, status, nil
}

// resolveVisibilityFlag normalizes the accepted "internal" alias to the
// canonical "shared" and validates the result against the canonical set. An
// empty value passes through (the server applies its configured default).
func resolveVisibilityFlag(visibility string) (string, error) {
	visibility = normalizeAccessLevel(visibility)
	if visibility != "" && !db.IsValidAppVisibility(visibility) {
		return "", fmt.Errorf("invalid --visibility %q: must be one of %s (internal is accepted as an alias for shared)",
			visibility, strings.Join(db.ValidAppVisibilities, ", "))
	}
	return visibility, nil
}

// ensureApp checks whether the app exists and creates it if not. When visibility
// is non-empty (one of "private", "shared", "public") it is forwarded in the
// creation request body; an empty string lets the server apply its configured
// default.
//
// If the app already exists and visibility is non-empty, a warning is printed to
// stderr — the flag is ignored for existing apps and the user should use
// `shinyhub apps access set` instead.
func ensureApp(cfg *cliConfig, slug, visibility string) error {
	return ensureAppWithOutput(cfg, slug, visibility, os.Stderr)
}

// ensureAppWithOutput is the testable core of ensureApp. errOut receives any
// warnings emitted during the call. The interactive `shinyhub deploy` path has
// no fleet manifest, so it always passes "" for the new app's project.
func ensureAppWithOutput(cfg *cliConfig, slug, visibility string, errOut io.Writer) error {
	return ensureAppCore(cfg, slug, visibility, "", errOut, true)
}

// ensureAppCore is the shared implementation. warnExisting controls whether a
// non-empty visibility on an already-existing app produces the corrective
// warning. The interactive `deploy` path sets it; the fleet path clears it,
// because fleet reconciles visibility through its own config-drift mechanism
// and the deploy-layer warning would otherwise leak once per retry.
func ensureAppCore(cfg *cliConfig, slug, visibility, project string, errOut io.Writer, warnExisting bool) error {
	checkReq, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	checkReq.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(checkReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		if visibility != "" && warnExisting {
			fmt.Fprintf(errOut, "warning: --visibility is ignored for existing apps; use `shinyhub apps access set %s %s` instead\n", slug, visibility)
		}
		return nil
	}

	createBody := map[string]string{"slug": slug, "name": slug}
	if visibility != "" {
		createBody["access"] = visibility
	}
	// Only on create, exactly like visibility: ensureAppCore returns early on
	// HTTP 200, so an existing app's project is never changed here. Reconciling
	// an existing app's project is applyConfigDrift's and reassertFleetConfig's
	// job.
	if project != "" {
		createBody["project_slug"] = project
	}
	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("encode create body: %w", err)
	}
	createReq, err := http.NewRequest("POST", cfg.Host+"/api/apps",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	createReq.Header.Set("Authorization", authHeader(cfg.Token))
	createReq.Header.Set("Content-Type", "application/json")
	cr, err := httpClient.Do(createReq)
	if err != nil {
		return err
	}
	defer cr.Body.Close()
	if cr.StatusCode != 201 {
		raw, _ := io.ReadAll(cr.Body)
		// Surface the server's `{"error": "..."}` envelope so the user gets
		// enough context to diagnose quota / permission / validation failures;
		// a lapsed session is reported as a re-login hint instead.
		return httpError(cfg.Token, "create app "+slug, cr, raw)
	}
	return nil
}

// gitClone shallow-clones repoURL at the given branch into a temp directory
// and returns the path. The caller is responsible for removing the directory.
func gitClone(repoURL, branch, subdir string) (string, error) {
	dir, err := os.MkdirTemp("", "shiny-git-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	args := []string{"clone", "--depth=1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, dir)

	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", gitCmdError("git clone", err, out)
	}

	if subdir != "" {
		appDir := filepath.Clean(filepath.Join(dir, subdir))
		rel, relErr := filepath.Rel(dir, appDir)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(subdir) {
			os.RemoveAll(dir)
			return "", fmt.Errorf("subdir %q must stay inside the repository", subdir)
		}
		info, err := os.Stat(appDir)
		if err != nil {
			os.RemoveAll(dir) // dir still holds the root clone path
			return "", fmt.Errorf("subdir %q not found in repo", subdir)
		}
		if !info.IsDir() {
			os.RemoveAll(dir)
			return "", fmt.Errorf("subdir %q is not a directory", subdir)
		}
		dir = appDir
	}

	return dir, nil
}

// bundlePreview is the exact archive deploy uploads plus deterministic,
// non-secret metadata used by `shinyhub plan`. Buffer is deliberately omitted
// from JSON. Digest uses the server's bundle digest algorithm.
type bundlePreview struct {
	Digest            string               `json:"digest"`
	FileCount         int                  `json:"file_count"`
	UncompressedBytes int64                `json:"uncompressed_bytes"`
	CompressedBytes   int                  `json:"compressed_bytes"`
	Files             []string             `json:"files"`
	IgnoreFile        string               `json:"ignore_file,omitempty"`
	IgnoredPaths      []string             `json:"ignored_paths,omitempty"`
	ProtectedPaths    []bundleSkippedPaths `json:"protected_paths,omitempty"`
	Buffer            *bytes.Buffer        `json:"-"`
}

type bundleSkippedPaths struct {
	Reason string   `json:"reason"`
	Paths  []string `json:"paths"`
}

func zipDir(dir string) (*bytes.Buffer, string, error) {
	preview, err := buildBundlePreview(dir)
	if err != nil {
		return nil, "", err
	}
	return preview.Buffer, summarizeDeploymentRejections(preview.ProtectedPaths), nil
}

func buildBundlePreview(dir string) (*bundlePreview, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	rules := bundle.DefaultRules()
	rejected := map[bundle.FilterDecision][]string{}
	preview := &bundlePreview{Files: []string{}, Buffer: &buf}

	matcher, ignoreHasNegation, matcherErr := loadIgnoreMatcher(dir)
	if matcherErr != nil {
		return nil, fmt.Errorf("load ignore file: %w", matcherErr)
	}
	if matcher != nil {
		if _, err := os.Stat(filepath.Join(dir, ".shinyhubignore")); err == nil {
			preview.IgnoreFile = ".shinyhubignore"
		} else {
			preview.IgnoreFile = ".gitignore"
		}
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		// Per-tree ignore filter runs before bundle.Rules so that ignore-file
		// rejections never show up in summarizeRejections (intentional excludes
		// are silent; only platform-policy rejections surface to the operator).
		if matcher != nil {
			query := relSlash
			if info.IsDir() {
				query = relSlash + "/"
			}
			if matcher.MatchesPath(query) {
				preview.IgnoredPaths = append(preview.IgnoredPaths, query)
				if info.IsDir() {
					// Only prune the subtree when no negation pattern could
					// re-include a descendant; otherwise descend and let
					// file-level matching handle each child.
					if !ignoreHasNegation {
						return filepath.SkipDir
					}
					return nil
				}
				return nil
			}
		}

		size := int64(0)
		if !info.IsDir() {
			size = info.Size()
		}
		decision := rules.Inspect(relSlash, size)
		switch decision {
		case bundle.FilterAccept:
			// fall through to include
		case bundle.FilterSkipCacheDir:
			rejected[decision] = append(rejected[decision], relSlash)
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		default:
			rejected[decision] = append(rejected[decision], relSlash)
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}
		preview.Files = append(preview.Files, relSlash)
		preview.UncompressedBytes += info.Size()
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = relSlash
		h.Method = zip.Deflate
		zw, err := w.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := io.Copy(zw, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	preview.FileCount = len(preview.Files)
	preview.CompressedBytes = buf.Len()
	sort.Strings(preview.Files)
	sort.Strings(preview.IgnoredPaths)
	for decision, paths := range rejected {
		sort.Strings(paths)
		preview.ProtectedPaths = append(preview.ProtectedPaths, bundleSkippedPaths{
			Reason: fmt.Sprint(decision), Paths: paths,
		})
	}
	sort.Slice(preview.ProtectedPaths, func(i, j int) bool {
		return preview.ProtectedPaths[i].Reason < preview.ProtectedPaths[j].Reason
	})
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	preview.Digest, err = bundle.DigestZipReader(zr)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

func summarizeSkippedPaths(groups []bundleSkippedPaths) string {
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.Paths) > 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", group.Reason, strings.Join(group.Paths, ", ")))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Skipped from bundle (push with `shinyhub data push`): " + strings.Join(parts, "; ")
}

func summarizeDeploymentRejections(groups []bundleSkippedPaths) string {
	filtered := make([]bundleSkippedPaths, 0, len(groups))
	for _, group := range groups {
		if group.Reason != bundle.FilterSkipCacheDir.String() {
			filtered = append(filtered, group)
		}
	}
	return summarizeSkippedPaths(filtered)
}

// loadIgnoreMatcher returns a gitignore-style matcher built from the first
// of .shinyhubignore or .gitignore found in dir. Returns (nil, false, nil)
// when neither file exists. The ignoreHasNegation bool reports whether the
// source file contains any negation patterns (`!`-prefixed lines), which
// determines whether directory matches can safely `filepath.SkipDir`-prune
// their subtree. Non-ENOENT read errors are surfaced rather than silently
// swallowed.
func loadIgnoreMatcher(dir string) (*gitignore.GitIgnore, bool, error) {
	for _, name := range []string{".shinyhubignore", ".gitignore"} {
		p := filepath.Join(dir, name)
		raw, err := os.ReadFile(p)
		if err == nil {
			matcher := gitignore.CompileIgnoreLines(strings.Split(string(raw), "\n")...)
			return matcher, ignoreFileHasNegation(raw), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return nil, false, nil
}

// ignoreFileHasNegation reports whether the gitignore-format content has any
// negation line (a non-comment, non-blank line whose first non-space rune is
// `!`). Used to decide if directory pruning is safe: when no negation patterns
// exist, a directory match means no descendant can be re-included, so
// filepath.SkipDir is correct. When negation patterns are present, the walker
// must descend and apply per-file matching instead.
func ignoreFileHasNegation(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "!") {
			return true
		}
	}
	return false
}

func summarizeRejections(r map[bundle.FilterDecision][]string) string {
	if len(r) == 0 {
		return ""
	}
	var parts []string
	for d, paths := range r {
		if len(paths) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", d, strings.Join(paths, ", ")))
	}
	sort.Strings(parts)
	return "Skipped from bundle (push with `shinyhub data push`): " + strings.Join(parts, "; ")
}
