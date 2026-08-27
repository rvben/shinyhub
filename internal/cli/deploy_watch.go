package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	deploypkg "github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/deployevent"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/rvben/shinyhub/internal/sourcewatch"
	"github.com/spf13/cobra"
)

var (
	errWatchBundleUnchanged       = errors.New("watch bundle is unchanged")
	watchPollInterval             = 500 * time.Millisecond
	watchSessionHeartbeatInterval = 30 * time.Second
	watchSessionEndTimeout        = 3 * time.Second
)

const (
	watchTargetExisting  = "existing"
	watchTargetCreated   = "created"
	watchTargetEphemeral = "ephemeral"
	minWatchTTL          = 15 * time.Minute
	maxWatchTTL          = 7 * 24 * time.Hour
)

var watchedSourceExcludes = []string{
	".git", ".shinyhub-run", ".venv", ".renv", ".Rproj.user",
	"__pycache__", "node_modules",
}

func runDeployWatch(cmd *cobra.Command, args []string, f *deployFlags) error {
	if f.git != "" {
		return validationErr("--watch does not support --git", "watch a local checkout by passing its directory instead")
	}
	if hostFlagOverride == "" && os.Getenv("SHINYHUB_HOST") == "" {
		return validationErr("--watch requires an explicit remote host", "pass --host <saved-dev-host> or set SHINYHUB_HOST")
	}
	if f.watchDelay < 100*time.Millisecond || f.watchDelay > time.Minute {
		return validationErr("--watch-delay must be between 100ms and 1m", "use a longer quiet period for editors that save files in bursts")
	}
	if f.create && f.ephemeral {
		return validationErr("--create and --ephemeral are mutually exclusive", "choose a persistent or temporary development target")
	}
	if f.ephemeral && (f.ttl < minWatchTTL || f.ttl > maxWatchTTL) {
		return validationErr("--ttl must be between 15m and 7d", "choose how long the temporary app should remain available")
	}
	if !f.ephemeral && cmd.Flags().Changed("ttl") {
		return validationErr("--ttl requires --ephemeral", "add --ephemeral or remove --ttl")
	}
	if f.ephemeral && f.visibility != "" && normalizeAccessLevel(f.visibility) != "private" {
		return validationErr("--ephemeral apps are always private", "remove --visibility or set it to private")
	}
	if !f.create && !f.ephemeral && f.visibility != "" {
		return validationErr("--visibility only applies when watch creates an app", "add --create, or change the existing app with `shinyhub apps access set`")
	}
	format := f.format
	if format == "" {
		var err error
		format, err = resolveFormat(false, true)
		if err != nil {
			return err
		}
	}

	// Resolve once before starting the long-lived loop. This proves the source
	// directory and slug are stable and gives the banner an exact target.
	if f.ephemeral && f.slug == "" {
		slug, err := generatedDevelopmentSlug(args)
		if err != nil {
			return err
		}
		f.slug = slug
	}
	source, err := resolveDeploymentSource(args, f)
	if err != nil {
		return err
	}
	defer source.cleanup()
	f.visibility = source.Visibility
	if f.bundleManifestRoot == "" {
		warnFleetCompositionOmission(cmd.ErrOrStderr(), source.Dir, fleetOmissionDeploy, quietFlag)
	}
	if err := validateRepeatedWatchHooks(source.Dir, f.allowRepeatedHooks); err != nil {
		return err
	}
	// Creation is explicit, but it should still be side-effect free until the
	// local source can produce a valid canonical deployment bundle. Without this
	// preflight a typo or malformed manifest could leave behind an empty app.
	if f.create || f.ephemeral {
		if _, _, err := prepareDeploymentForFlags(source.Dir, f); err != nil {
			return err
		}
	}
	cfg, err := loadDeployConfig(cmd)
	if err != nil {
		return err
	}
	sessionID, err := newDevelopmentSessionID()
	if err != nil {
		return fmt.Errorf("create development session: %w", err)
	}
	f.developmentSessionID = sessionID
	if err := prepareWatchTarget(cfg, source.Slug, f); err != nil {
		return err
	}
	defer func() {
		if endErr := endDevelopmentSession(cfg, source.Slug, sessionID); endErr != nil && !quietFlag {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not close the remote development session immediately: %v; the server lease will close it automatically.\n", endErr)
		}
	}()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	previousContext := cmd.Context()
	cmd.SetContext(ctx)
	defer cmd.SetContext(previousContext)
	if err := heartbeatDevelopmentSession(ctx, cfg, source.Slug, sessionID, f.developmentTarget); err != nil {
		return fmt.Errorf("start remote development session: %w", err)
	}
	leaseErrors := make(chan error, 1)
	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	go maintainDevelopmentSessionLease(leaseCtx, cfg, source.Slug, sessionID, f.developmentTarget, leaseErrors)

	changes := sourcewatch.Changes(ctx, source.Dir, sourcewatch.Options{
		Interval:      watchPollInterval,
		ExcludeDirs:   watchedSourceExcludes,
		ExternalFiles: f.watchExternalFiles,
	})
	f.watchMode = true
	f.deployChannel = "watch"
	f.format = format
	f.start = true
	f.wait = true

	if err := writeWatchStarted(cmd, format, source, cfg, f); err != nil {
		return err
	}
	first := true
	for {
		if !first {
			if err := writeWatchDeploying(cmd, format); err != nil {
				return err
			}
		}
		deployErr := runDeploy(cmd, args, f)
		switch {
		case deployErr == nil:
			// --open is useful for the first successful deployment, but opening a
			// new tab after every save is never useful. Visibility likewise only
			// affects creation and would otherwise warn on every redeploy.
			f.open = false
			f.visibility = ""
		case errors.Is(deployErr, errWatchBundleUnchanged):
			if err := writeWatchUnchanged(cmd, format); err != nil {
				return err
			}
		case ctx.Err() != nil:
			writeWatchStopped(cmd, format)
			return nil
		case watchErrorIsFatal(deployErr):
			return deployErr
		default:
			if err := writeWatchFailure(cmd, format, deployErr); err != nil {
				return err
			}
		}
		// A committed deployment can still report a follow-up failure while
		// waiting for readiness or opening the app. Once a digest is committed,
		// creation-only visibility no longer applies to subsequent attempts.
		if f.watchLastDigest != "" {
			f.visibility = ""
		}
		first = false
		if !quietFlag && format == formatTable {
			fmt.Fprintln(cmd.ErrOrStderr(), "Watching for changes…")
		}
		changed, leaseErr := waitForDebouncedWatchChangeWithLease(ctx, changes, f.watchDelay, leaseErrors)
		if leaseErr != nil {
			return leaseErr
		}
		if !changed {
			writeWatchStopped(cmd, format)
			return nil
		}
	}
}

func validateRepeatedWatchHooks(dir string, allowed bool) error {
	if allowed {
		return nil
	}
	manifest, err := deploypkg.LoadManifest(dir)
	if err != nil {
		// Canonical bundle preparation reports malformed manifests. Returning nil
		// here lets the watch loop stay alive while the developer fixes the file.
		return nil
	}
	if manifest == nil || len(manifest.Hooks) == 0 {
		return nil
	}
	return validationErr(
		fmt.Sprintf("--watch would run %d post-deploy hook(s) after every deployable change", len(manifest.Hooks)),
		"review whether the hooks are safe to repeat, then add --allow-repeated-hooks",
	)
}

func waitForDebouncedWatchChange(ctx context.Context, changes <-chan struct{}, delay time.Duration) bool {
	changed, _ := waitForDebouncedWatchChangeWithLease(ctx, changes, delay, nil)
	return changed
}

func waitForDebouncedWatchChangeWithLease(ctx context.Context, changes <-chan struct{}, delay time.Duration, leaseErrors <-chan error) (bool, error) {
	select {
	case <-ctx.Done():
		return false, nil
	case err := <-leaseErrors:
		return false, err
	case _, ok := <-changes:
		if !ok {
			return false, nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case err := <-leaseErrors:
			return false, err
		case _, ok := <-changes:
			if !ok {
				return false, nil
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		case <-timer.C:
			return true, nil
		}
	}
}

func watchErrorIsFatal(err error) bool {
	var httpErr *deployHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.fatal()
	}
	kind, _ := classify(err)
	return kind == KindAuth || kind == KindValidation || kind == KindNotFound
}

func writeWatchStarted(cmd *cobra.Command, format outputFormat, source *deploymentSource, cfg *cliConfig, f *deployFlags) error {
	target := remoteAppURL(cfg.Host, source.Slug)
	if format == formatNDJSON {
		e := deployevent.Phase("watch", deployevent.StatusStarted, "Watching local source and deploying remote changes")
		return writeDeployEvent(cmd.OutOrStdout(), e)
	}
	if quietFlag {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Remote development\n  Source: %s\n  Target: %s\n  App: %s\n  Quiet period: %s\n", source.Dir, target, watchTargetDescription(f), f.watchDelay)
	if f.developmentExpiresAt != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Expires: %s\n", f.developmentExpiresAt.Local().Format(time.RFC1123))
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Each deploy is recorded; failed candidates trigger the server's previous-version recovery. Press Ctrl-C to stop.")
	return nil
}

func newDevelopmentSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func generatedDevelopmentSlug(args []string) (string, error) {
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve app directory: %w", err)
	}
	base := sanitizeSlug(filepath.Base(abs))
	return generatedDevelopmentSlugForBase(base)
}

func generatedDevelopmentSlugForBase(base string) (string, error) {
	if base == "" {
		base = "app"
	}
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	suffixText := hex.EncodeToString(suffix[:])
	maxBase := slugpkg.MaxLen - len("-dev-") - len(suffixText)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	return base + "-dev-" + suffixText, nil
}

func prepareWatchTarget(cfg *cliConfig, slug string, f *deployFlags) error {
	exists, err := watchTargetExists(cfg, slug)
	if err != nil {
		return err
	}
	switch {
	case f.create:
		f.developmentTarget = watchTargetCreated
		if exists {
			return validationErr("app "+slug+" already exists", "remove --create to attach the watch session to it")
		}
		return createWatchTarget(cfg, slug, f)
	case f.ephemeral:
		f.developmentTarget = watchTargetEphemeral
		if exists {
			return validationErr("app "+slug+" already exists", "choose another --slug or omit it to generate a temporary slug")
		}
		expires := time.Now().UTC().Add(f.ttl)
		f.developmentExpiresAt = &expires
		f.visibility = "private"
		return createWatchTarget(cfg, slug, f)
	default:
		f.developmentTarget = watchTargetExisting
		if !exists {
			return validationErr("app "+slug+" does not exist", "create it explicitly with --watch --create, or use --ephemeral for a temporary app")
		}
		return nil
	}
}

func watchTargetExists(cfg *cliConfig, slug string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return false, httpError(cfg.Token, "check app "+slug, resp, raw)
}

func createWatchTarget(cfg *cliConfig, slug string, f *deployFlags) error {
	body := map[string]any{
		"slug": slug, "name": slug, "development_session_id": f.developmentSessionID,
		"development_target": f.developmentTarget,
	}
	if f.visibility != "" {
		body["access"] = normalizeAccessLevel(f.visibility)
	}
	if f.developmentExpiresAt != nil {
		body["expires_at"] = f.developmentExpiresAt.Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Host+"/api/apps", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return httpError(cfg.Token, "create development app "+slug, resp, responseBody)
	}
	return nil
}

func developmentSessionLeaseRequest(ctx context.Context, cfg *cliConfig, slug, sessionID, target, action string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.Host+"/api/apps/"+slug+"/development-sessions/"+sessionID+"/"+action, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if action == "heartbeat" {
		req.Header.Set("X-Shinyhub-Deploy-Channel", "watch")
		req.Header.Set("X-Shinyhub-Development-Session", sessionID)
		req.Header.Set("X-Shinyhub-Development-Target", target)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// The app and its session disappear together. A 404 while ending therefore
	// already represents the desired terminal state.
	if action == "end" && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return httpError(cfg.Token, action+" development session", resp, body)
}

func heartbeatDevelopmentSession(ctx context.Context, cfg *cliConfig, slug, sessionID, target string) error {
	return developmentSessionLeaseRequest(ctx, cfg, slug, sessionID, target, "heartbeat")
}

func maintainDevelopmentSessionLease(ctx context.Context, cfg *cliConfig, slug, sessionID, target string, fatal chan<- error) {
	ticker := time.NewTicker(watchSessionHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := heartbeatDevelopmentSession(ctx, cfg, slug, sessionID, target)
			if err == nil {
				continue
			}
			var statusErr *httpStatusError
			if errors.As(err, &statusErr) && statusErr.Status >= 400 && statusErr.Status < 500 && statusErr.Status != http.StatusTooManyRequests {
				select {
				case fatal <- fmt.Errorf("remote development session lease was rejected: %w", err):
				default:
				}
				return
			}
			// Transport, rate-limit, and server failures are transient. Keep the
			// watcher alive; if they outlast the lease, the next successful request
			// receives a conflict and takes the fatal path above.
		}
	}
}

func endDevelopmentSession(cfg *cliConfig, slug, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), watchSessionEndTimeout)
	defer cancel()
	return developmentSessionLeaseRequest(ctx, cfg, slug, sessionID, "", "end")
}

func watchTargetDescription(f *deployFlags) string {
	switch f.developmentTarget {
	case watchTargetCreated:
		return "new persistent app"
	case watchTargetEphemeral:
		return "temporary private app"
	default:
		return "existing app"
	}
}

func writeWatchDeploying(cmd *cobra.Command, format outputFormat) error {
	if format == formatNDJSON {
		return writeDeployEvent(cmd.OutOrStdout(), deployevent.Phase("watch", deployevent.StatusProgress, "Deployable source change detected"))
	}
	if !quietFlag {
		fmt.Fprintln(cmd.ErrOrStderr(), "\nChange detected; preparing deployment…")
	}
	return nil
}

func writeWatchUnchanged(cmd *cobra.Command, format outputFormat) error {
	if format == formatNDJSON {
		return writeDeployEvent(cmd.OutOrStdout(), deployevent.Phase("watch", deployevent.StatusCompleted, "Source changed, but the deployable bundle is unchanged"))
	}
	if !quietFlag {
		fmt.Fprintln(cmd.ErrOrStderr(), "No deployable changes; ignored or generated files did not alter the bundle.")
	}
	return nil
}

func writeWatchFailure(cmd *cobra.Command, format outputFormat, err error) error {
	message := err.Error()
	var hinted hintedError
	if errors.As(err, &hinted) && hinted.Hint() != "" {
		message += ". Hint: " + hinted.Hint()
	}
	if format == formatNDJSON {
		var exitErr *ExitCodeError
		if errors.As(err, &exitErr) && exitErr.Reported {
			return nil
		}
		kind, _ := classify(err)
		return writeDeployEvent(cmd.OutOrStdout(), deployevent.Event{
			Type: deployevent.TypeError, Phase: "watch", Message: message, FailureKind: string(kind),
		})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Deploy failed: %v\nThe watch process is still running; fix the source and save again.\n", err)
	if errors.As(err, &hinted) && hinted.Hint() != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Hint: %s\n", hinted.Hint())
	}
	return nil
}

func writeWatchStopped(cmd *cobra.Command, format outputFormat) {
	if format == formatNDJSON {
		_ = writeDeployEvent(cmd.OutOrStdout(), deployevent.Phase("watch", deployevent.StatusCompleted, "Stopped watching for remote changes"))
		return
	}
	if !quietFlag {
		fmt.Fprintln(cmd.ErrOrStderr(), "Stopped remote development watch.")
	}
}
