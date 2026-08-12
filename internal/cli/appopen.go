package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

type appLaunchState struct {
	Slug             string
	Status           string
	Access           string
	DeployCount      int
	RedeployInFlight bool
}

func remoteAppURL(host, slug string) string {
	return strings.TrimRight(host, "/") + "/app/" + url.PathEscape(slug) + "/"
}

func fetchAppLaunchState(cfg *cliConfig, slug string) (appLaunchState, error) {
	var state appLaunchState
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/api/apps/"+url.PathEscape(slug), nil)
	if err != nil {
		return state, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusBadRequest {
		return state, httpError(cfg.Token, "open app", resp, body)
	}
	var envelope struct {
		App struct {
			Slug        string `json:"slug"`
			Status      string `json:"status"`
			Access      string `json:"access"`
			DeployCount int    `json:"deploy_count"`
		} `json:"app"`
		RedeployInFlight bool `json:"redeploy_in_flight"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return state, protocolFailure(cfg, fmt.Errorf("decode app state: %w", err))
	}
	return appLaunchState{
		Slug: envelope.App.Slug, Status: strings.ToLower(envelope.App.Status),
		Access: envelope.App.Access, DeployCount: envelope.App.DeployCount,
		RedeployInFlight: envelope.RedeployInFlight,
	}, nil
}

func appStatusOpenable(status string) bool {
	switch strings.ToLower(status) {
	case "running", "healthy", "hibernated", "suspended", "deploying", "waking", "degraded":
		return true
	default:
		return false
	}
}

// verifyPublicAppRoute proves that the user-facing route answers, not merely
// that the API row says running. Private/shared routes cannot be probed with a
// CLI credential by design: /app/* reserves Authorization for the embedded app
// and authenticates people through their browser session.
func verifyPublicAppRoute(target string) error {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("build route check: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
	}
	return &httpStatusError{Status: resp.StatusCode,
		msg: fmt.Sprintf("public app route returned %s", resp.Status)}
}

// openAppURL treats launching a browser as a convenience, never as evidence
// that deployment failed. A headless host or missing opener falls back to a
// copyable URL and reports opened=false to structured callers.
func openAppURL(target string, noBrowser bool, errOut io.Writer) bool {
	if noBrowser {
		return false
	}
	if err := openBrowserURL(target); err != nil {
		fmt.Fprintf(errOut, "Browser could not be opened automatically: %v\nOpen: %s\n", err, target)
		return false
	}
	return true
}

func appOpenStateError(slug string, state appLaunchState) error {
	if state.DeployCount == 0 {
		return validationErr(fmt.Sprintf("%s has not been deployed yet", slug),
			fmt.Sprintf("deploy it first with `shinyhub deploy . --slug %s --open`", slug))
	}
	switch state.Status {
	case "stopped":
		return validationErr(fmt.Sprintf("%s is stopped", slug),
			fmt.Sprintf("start it with `shinyhub apps start %s`, then run `shinyhub apps open %s`", slug, slug))
	case "crashed", "failed":
		return validationErr(fmt.Sprintf("%s is %s and cannot be opened", slug, state.Status),
			fmt.Sprintf("inspect the failure with `shinyhub apps logs %s --no-follow`, then restart or deploy a fix", slug))
	default:
		return validationErr(fmt.Sprintf("%s is not currently openable (status: %s)", slug, emptyAs(state.Status, "unknown")),
			fmt.Sprintf("inspect it with `shinyhub apps show %s`", slug))
	}
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type appsOpenFlags struct {
	noBrowser bool
}

func newAppsOpenCmd() *cobra.Command {
	f := &appsOpenFlags{}
	cmd := &cobra.Command{
		Use:   "open <slug>",
		Short: "Open an app in the default browser",
		Long: `Open an existing app through its user-facing /app/<slug>/ route.

Running, sleeping, waking, deploying, and degraded apps are openable; sleeping
apps wake through the normal browser flow. A stopped, crashed, or never-deployed
app is not opened and the command names the exact recovery command. Public app
routes are smoke-tested before the browser launches. Private and shared apps
open into the normal browser sign-in flow.

Use --no-browser on a headless machine to print the URL without launching it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsOpen(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "Print the app URL instead of launching a browser")
	return cmd
}

func runAppsOpen(cmd *cobra.Command, args []string, f *appsOpenFlags) error {
	format, err := resolveFormat(false, false)
	if err != nil {
		return err
	}
	slug := args[0]
	if !slugpkg.Valid(slug) {
		return validationErr(fmt.Sprintf("invalid app slug %q", slug), "app slugs must be "+slugpkg.HumanRule)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	state, err := fetchAppLaunchState(cfg, slug)
	if err != nil {
		return err
	}
	if !appStatusOpenable(state.Status) {
		return appOpenStateError(slug, state)
	}
	target := remoteAppURL(cfg.Host, slug)
	if state.Access == "public" && (state.Status == "running" || state.Status == "healthy" || state.Status == "degraded") {
		if err := verifyPublicAppRoute(target); err != nil {
			return &hintedMsgError{msg: fmt.Sprintf("%s is openable in the API, but its public route check failed: %v", slug, err),
				hint: fmt.Sprintf("the app was not changed; inspect `shinyhub apps logs %s --no-follow` and retry %s", slug, target), cause: err}
		}
	}
	opened := openAppURL(target, f.noBrowser, cmd.ErrOrStderr())
	result := map[string]any{
		"status": "ready", "slug": slug, "url": target, "opened": opened,
		"app_status": state.Status,
	}
	if format == formatJSON {
		if opened {
			fmt.Fprintf(cmd.ErrOrStderr(), "Opened in your browser: %s\n", target)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	ps := stylerFor(cmd.OutOrStdout())
	if opened {
		fmt.Fprintf(cmd.OutOrStdout(), "%sOpened %s\n", ps.okPrefix(), slug)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s is ready\n", slug)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "URL: %s\n", target)
	if state.Status == "hibernated" || state.Status == "suspended" || state.Status == "waking" {
		fmt.Fprintln(cmd.ErrOrStderr(), "The app is sleeping; opening this URL wakes it automatically.")
	} else if state.Status == "deploying" || state.Status == "degraded" || state.RedeployInFlight {
		fmt.Fprintf(cmd.ErrOrStderr(), "The app is %s; ShinyHub will route the browser to an available replica.\n", state.Status)
	}
	return nil
}
