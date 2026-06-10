package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newAppsCmd builds a fresh `apps` command tree each time it is called. Every
// subcommand binds its flags to a per-instance struct (no package-level flag
// state) so repeated or shuffled test runs cannot leak flag values or cobra
// Changed markers between each other.
func newAppsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "apps", Short: "Manage apps"}
	cmd.AddCommand(
		newAppsListCmd(),
		newAppsShowCmd(),
		newAppsLogsCmd(),
		newAppsRollbackCmd(),
		newAppsRestartCmd(),
		newAppsStartCmd(),
		newAppsSetCmd(),
		newAppsAccessCmd(),
		newAppsDeleteCmd(),
		newAppsStopCmd(),
		newAppsDeploymentsCmd(),
	)
	return cmd
}

// newTokensCmd builds a fresh `tokens` command tree each time it is called.
func newTokensCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tokens", Short: "Manage API tokens"}
	cmd.AddCommand(newTokensCreateCmd(), newTokensListCmd(), newTokensRevokeCmd())
	return cmd
}

// ── apps list ───────────────────────────────────────────────────────────────

type appsListFlags struct {
	jsonOutput bool
}

func newAppsListCmd() *cobra.Command {
	f := &appsListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsList(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsList(cmd *cobra.Command, args []string, f *appsListFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "list apps", resp, out)
	}

	if f.jsonOutput {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var apps []map[string]any
	if err := json.Unmarshal(out, &apps); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(apps) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No apps.")
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-20s %-10s %-12s\n", "SLUG", "STATUS", "DEPLOYS")
	for _, a := range apps {
		row := fmt.Sprintf("%-20s %-10s %-12v", a["slug"], a["status"], a["deploy_count"])
		fmt.Fprintln(w, strings.TrimRight(row, " "))
	}
	return nil
}

// ── apps show ───────────────────────────────────────────────────────────────

type appsShowFlags struct {
	jsonOutput bool
}

func newAppsShowCmd() *cobra.Command {
	f := &appsShowFlags{}
	cmd := &cobra.Command{
		Use:   "show <slug>",
		Short: "Show detailed information about an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsShow(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsShow(cmd *cobra.Command, args []string, f *appsShowFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "show app", resp, out)
	}

	if f.jsonOutput {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var resp2 struct {
		App struct {
			Slug                    string  `json:"slug"`
			Name                    string  `json:"name"`
			OwnerID                 int64   `json:"owner_id"`
			Access                  string  `json:"access"`
			Status                  string  `json:"status"`
			Replicas                int     `json:"replicas"`
			MaxSessionsPerReplica   int     `json:"max_sessions_per_replica"`
			DeployCount             int     `json:"deploy_count"`
			HibernateTimeoutMinutes *int    `json:"hibernate_timeout_minutes"`
			MemoryLimitMB           *int    `json:"memory_limit_mb"`
			CPUQuotaPercent         *int    `json:"cpu_quota_percent"`
			ProjectSlug             string  `json:"project_slug,omitempty"`
			CreatedAt               string  `json:"created_at"`
			UpdatedAt               string  `json:"updated_at"`
			AutoscaleEnabled        bool    `json:"autoscale_enabled"`
			AutoscaleMinReplicas    int     `json:"autoscale_min_replicas"`
			AutoscaleMaxReplicas    int     `json:"autoscale_max_replicas"`
			AutoscaleTarget         float64 `json:"autoscale_target"`
		} `json:"app"`
		EffectiveMaxSessionsPerReplica *int     `json:"effective_max_sessions_per_replica"`
		EffectiveAutoscaleTarget       *float64 `json:"effective_autoscale_target"`
		ReplicasStatus                 []struct {
			Index  int    `json:"index"`
			Status string `json:"status"`
			PID    *int   `json:"pid"`
			Port   *int   `json:"port"`
			Reason string `json:"reason"`
		} `json:"replicas_status"`
		RejectsByReason *struct {
			WindowSeconds int               `json:"window_seconds"`
			Counts        map[string]uint64 `json:"counts"`
		} `json:"rejects_by_reason"`
	}
	if err := json.Unmarshal(out, &resp2); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	w := cmd.OutOrStdout()
	a := resp2.App
	fmt.Fprintf(w, "Slug:        %s\n", a.Slug)
	fmt.Fprintf(w, "Name:        %s\n", a.Name)
	fmt.Fprintf(w, "Status:      %s\n", a.Status)
	fmt.Fprintf(w, "Access:      %s\n", a.Access)
	fmt.Fprintf(w, "Owner:       user #%d\n", a.OwnerID)
	if a.ProjectSlug != "" {
		fmt.Fprintf(w, "Project:     %s\n", a.ProjectSlug)
	}
	fmt.Fprintf(w, "Deploys:     %d\n", a.DeployCount)
	fmt.Fprintf(w, "Replicas:    %d\n", a.Replicas)
	// effective cap resolves the per-app value against the runtime default (0 =
	// inherit). Annotate a 0 with the resolved default and print the admission
	// ceiling (replicas × effective cap) so the bare "0" is not cryptic. An
	// older server may omit effective_max_sessions_per_replica entirely; treat
	// the absent field distinctly from a reported 0 so we never invent an
	// "unlimited" ceiling for an app that has an explicit cap.
	effKnown := resp2.EffectiveMaxSessionsPerReplica != nil
	if a.MaxSessionsPerReplica == 0 {
		if effKnown {
			fmt.Fprintf(w, "Max sess/r:  0 (runtime default: %d)\n", *resp2.EffectiveMaxSessionsPerReplica)
		} else {
			fmt.Fprintf(w, "Max sess/r:  0 (runtime default)\n")
		}
	} else {
		fmt.Fprintf(w, "Max sess/r:  %d\n", a.MaxSessionsPerReplica)
	}
	// Prefer the server-resolved effective cap; when it is absent fall back to
	// the explicit app cap. If neither is known (absent effective + inherited
	// cap), the ceiling is unresolvable client-side, so omit it rather than
	// claim unlimited.
	switch {
	case effKnown:
		if eff := *resp2.EffectiveMaxSessionsPerReplica; eff == 0 {
			fmt.Fprintf(w, "Admission ceiling: unlimited (no session cap)\n")
		} else {
			fmt.Fprintf(w, "Admission ceiling: %d × %d = %d concurrent new sessions\n", a.Replicas, eff, a.Replicas*eff)
		}
	case a.MaxSessionsPerReplica > 0:
		eff := a.MaxSessionsPerReplica
		fmt.Fprintf(w, "Admission ceiling: %d × %d = %d concurrent new sessions\n", a.Replicas, eff, a.Replicas*eff)
	}
	// Autoscale summary: when enabled, resolve the effective target (the app's
	// own value, or the runtime default the server reports when the app's is 0)
	// so a 0 never reads as a literal "0%".
	if a.AutoscaleEnabled {
		target := a.AutoscaleTarget
		if resp2.EffectiveAutoscaleTarget != nil {
			target = *resp2.EffectiveAutoscaleTarget
		}
		fmt.Fprintf(w, "Autoscale:   on (replicas %d-%d, target %.0f%%)\n",
			a.AutoscaleMinReplicas, a.AutoscaleMaxReplicas, target*100)
	} else {
		fmt.Fprintln(w, "Autoscale:   off")
	}
	if rr := resp2.RejectsByReason; rr != nil && len(rr.Counts) > 0 {
		mins := rr.WindowSeconds / 60
		fmt.Fprintf(w, "rejects (last %dm):\n", mins)
		// Stable ordering so the output is deterministic.
		reasons := make([]string, 0, len(rr.Counts))
		for reason := range rr.Counts {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(w, "  %s: %d\n", reason, rr.Counts[reason])
		}
	}
	if a.HibernateTimeoutMinutes != nil {
		fmt.Fprintf(w, "Hibernate:   %d min\n", *a.HibernateTimeoutMinutes)
	} else {
		fmt.Fprintln(w, "Hibernate:   (global default)")
	}
	if a.MemoryLimitMB != nil {
		fmt.Fprintf(w, "Memory:      %d MB\n", *a.MemoryLimitMB)
	}
	if a.CPUQuotaPercent != nil {
		fmt.Fprintf(w, "CPU:         %d%%\n", *a.CPUQuotaPercent)
	}
	if len(a.CreatedAt) >= 10 {
		fmt.Fprintf(w, "Created:     %s\n", a.CreatedAt[:10])
	}
	if len(resp2.ReplicasStatus) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Replicas:")
		fmt.Fprintf(w, "  %-6s %-10s %-8s %s\n", "INDEX", "STATUS", "PID", "PORT")
		for _, r := range resp2.ReplicasStatus {
			pid, port := "-", "-"
			if r.PID != nil {
				pid = fmt.Sprintf("%d", *r.PID)
			}
			if r.Port != nil {
				port = fmt.Sprintf("%d", *r.Port)
			}
			reason := ""
			if r.Reason != "" {
				reason = "  (" + r.Reason + ")"
			}
			fmt.Fprintf(w, "  %-6d %-10s %-8s %s%s\n", r.Index, r.Status, pid, port, reason)
		}
	}
	return nil
}

// ── apps logs ───────────────────────────────────────────────────────────────

type appsLogsFlags struct {
	tail     int
	noFollow bool
	replica  int
}

func newAppsLogsCmd() *cobra.Command {
	f := &appsLogsFlags{}
	cmd := &cobra.Command{
		Use:   "logs <slug>",
		Short: "Stream or fetch logs for an app",
		Long: "Stream or fetch logs for an app.\n" +
			"\n" +
			"By default this opens a Server-Sent Events stream that emits the last 200\n" +
			"lines then follows new output until interrupted. Pass --no-follow to get a\n" +
			"one-shot plain-text response (kubectl/docker-style) that prints the tail and\n" +
			"exits - suitable for CI and grep pipelines.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsLogs(cmd, args, f)
		},
	}
	cmd.Flags().IntVar(&f.tail, "tail", 200,
		"Number of initial lines to emit (1..10000)")
	cmd.Flags().BoolVar(&f.noFollow, "no-follow", false,
		"Print the tail and exit instead of streaming new output")
	cmd.Flags().IntVar(&f.replica, "replica", 0,
		"Replica index (default 0)")
	return cmd
}

func runAppsLogs(cmd *cobra.Command, args []string, f *appsLogsFlags) error {
	if f.tail <= 0 || f.tail > 10000 {
		return fmt.Errorf("--tail must be between 1 and 10000")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/apps/%s/logs?tail=%d&replica=%d",
		cfg.Host, args[0], f.tail, f.replica)
	if f.noFollow {
		url += "&follow=false"
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))

	// One-shot fetch (--no-follow) uses the bounded-timeout client so a
	// stalled server doesn't pin the CLI forever. The streaming path uses
	// the default client (no timeout) since SSE connections are long-lived
	// by design.
	client := http.DefaultClient
	if f.noFollow {
		req.Header.Set("Accept", "text/plain")
		client = httpClient
	} else {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "stream logs", resp, out)
	}

	w := cmd.OutOrStdout()
	if f.noFollow {
		_, err := io.Copy(w, resp.Body)
		return err
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fmt.Fprintln(w, scanner.Text())
	}
	return scanner.Err()
}

// ── apps rollback ───────────────────────────────────────────────────────────

type rollbackFlags struct {
	deploymentID int64
}

func newAppsRollbackCmd() *cobra.Command {
	f := &rollbackFlags{}
	cmd := &cobra.Command{
		Use:   "rollback <slug>",
		Short: "Roll back an app to the previous or a specific historical deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsRollback(cmd, args, f)
		},
	}
	cmd.Flags().Int64Var(&f.deploymentID, "to", 0,
		"Deployment ID to roll back to (default: previous deployment)")
	return cmd
}

func runAppsRollback(cmd *cobra.Command, args []string, f *rollbackFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

	var bodyReader io.Reader
	if cmd.Flags().Changed("to") {
		body, err := json.Marshal(map[string]any{"deployment_id": f.deploymentID})
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/rollback", bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "rollback", resp, out)
	}
	if cmd.Flags().Changed("to") {
		fmt.Printf("%s: rolled back to deployment %d\n", slug, f.deploymentID)
	} else {
		fmt.Printf("%s: rolled back to previous deployment\n", slug)
	}
	return nil
}

// ── apps restart / start ────────────────────────────────────────────────────

func newAppsRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <slug>",
		Short: "Restart a running app",
		Args:  cobra.ExactArgs(1),
		RunE:  rollbackOrRestart("restart", "POST"),
	}
}

// newAppsStartCmd is a friendlier alias for `apps restart`. The server's
// restart endpoint redeploys the current bundle whether the app is running or
// stopped, so it is also the right verb for "bring this stopped app back up".
func newAppsStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <slug>",
		Short: "Start a stopped app (alias for `restart`)",
		Args:  cobra.ExactArgs(1),
		RunE:  callRestartAs("started"),
	}
}

// callRestartAs hits POST /api/apps/{slug}/restart but reports the action
// using a different past-tense verb (e.g. "started" instead of "restarted")
// so `apps start` reads naturally without duplicating the HTTP plumbing.
func callRestartAs(pastTense string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		slug := args[0]
		req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/restart", nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "start app", resp, out)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", slug, pastTense)
		return nil
	}
}

func rollbackOrRestart(action, method string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		slug := args[0]
		req, err := http.NewRequest(method, cfg.Host+"/api/apps/"+slug+"/"+action, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, action, resp, out)
		}
		fmt.Printf("%s: %s\n", slug, action+"ed")
		return nil
	}
}

// ── apps set ────────────────────────────────────────────────────────────────

type appsSetFlags struct {
	hibernateTimeout      int
	replicas              int
	maxSessionsPerReplica int
	minWarmReplicas       int
	tiers                 []string
	autoscale             bool
	autoscaleMin          int
	autoscaleMax          int
	autoscaleTarget       float64
	yes                   bool
	wait                  bool
	waitTimeout           time.Duration
}

func newAppsSetCmd() *cobra.Command {
	f := &appsSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <slug>",
		Short: "Update app settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsSet(cmd, args, f)
		},
	}
	cmd.Flags().IntVar(&f.hibernateTimeout, "hibernate-timeout", 0,
		"Idle timeout minutes before hibernation (-1 = reset to global default, 0 = disable, N = N minutes)")
	cmd.Flags().IntVar(&f.replicas, "replicas", 0,
		"Number of replica processes serving this app (>= 1)")
	cmd.Flags().IntVar(&f.maxSessionsPerReplica, "max-sessions-per-replica", 0,
		"Per-replica new-session admission cap (0 = runtime default; 1..1000 = explicit)")
	cmd.Flags().IntVar(&f.minWarmReplicas, "min-warm-replicas", 0,
		"Pre-warming floor: replicas kept running during idle hibernation (0..1000)")
	cmd.Flags().StringArrayVar(&f.tiers, "tier", nil,
		"Per-tier replica placement as name=count (repeatable, e.g. --tier local=2 --tier burst=1); mutually exclusive with --replicas")
	cmd.Flags().BoolVar(&f.autoscale, "autoscale", false,
		"Enable (--autoscale) or disable (--autoscale=false) session-saturation replica autoscaling for this app")
	cmd.Flags().IntVar(&f.autoscaleMin, "autoscale-min", 0,
		"Autoscale floor: minimum replicas to keep running (>= 1)")
	cmd.Flags().IntVar(&f.autoscaleMax, "autoscale-max", 0,
		"Autoscale ceiling: maximum replicas the controller may add (>= autoscale-min)")
	cmd.Flags().Float64Var(&f.autoscaleTarget, "autoscale-target", 0,
		"Target average fraction of the per-replica session cap, in (0,1] (0 = inherit the runtime default)")
	cmd.Flags().BoolVar(&f.yes, "yes", false,
		"Skip the confirmation prompt for a replica change (which restarts the app)")
	cmd.Flags().BoolVar(&f.wait, "wait", false,
		"After applying, wait until the app is healthy again (useful after a replica change)")
	cmd.Flags().DurationVar(&f.waitTimeout, "wait-timeout", 300*time.Second,
		"How long to wait for the app to become healthy when --wait is set")
	return cmd
}

func runAppsSet(cmd *cobra.Command, args []string, f *appsSetFlags) error {
	hibernateChanged := cmd.Flags().Changed("hibernate-timeout")
	replicasChanged := cmd.Flags().Changed("replicas")
	capChanged := cmd.Flags().Changed("max-sessions-per-replica")
	minWarmReplicasChanged := cmd.Flags().Changed("min-warm-replicas")
	tierChanged := cmd.Flags().Changed("tier")
	autoscaleChanged := cmd.Flags().Changed("autoscale")
	autoscaleMinChanged := cmd.Flags().Changed("autoscale-min")
	autoscaleMaxChanged := cmd.Flags().Changed("autoscale-max")
	autoscaleTargetChanged := cmd.Flags().Changed("autoscale-target")
	anyAutoscaleChanged := autoscaleChanged || autoscaleMinChanged || autoscaleMaxChanged || autoscaleTargetChanged

	if !hibernateChanged && !replicasChanged && !capChanged && !minWarmReplicasChanged && !tierChanged && !anyAutoscaleChanged {
		return fmt.Errorf("at least one flag is required (e.g. --hibernate-timeout, --replicas, --tier, --max-sessions-per-replica, --min-warm-replicas, --autoscale)")
	}
	if replicasChanged && f.replicas < 1 {
		return fmt.Errorf("--replicas must be >= 1")
	}
	if capChanged && (f.maxSessionsPerReplica < 0 || f.maxSessionsPerReplica > 1000) {
		return fmt.Errorf("--max-sessions-per-replica must be between 0 and 1000")
	}
	if minWarmReplicasChanged && (f.minWarmReplicas < 0 || f.minWarmReplicas > 1000) {
		return fmt.Errorf("--min-warm-replicas must be between 0 and 1000")
	}
	if hibernateChanged && f.hibernateTimeout < -1 {
		return fmt.Errorf("--hibernate-timeout must be -1 (reset to global default), 0 (disable), or a positive number of minutes")
	}
	// Validate the autoscale flags client-side for fast feedback; the server
	// remains the authority on the cross-field rules (min >= 1 when enabled,
	// max <= the runtime ceiling) since those depend on server config.
	if autoscaleTargetChanged && (f.autoscaleTarget < 0 || f.autoscaleTarget > 1) {
		return fmt.Errorf("--autoscale-target must be in [0,1] (0 inherits the runtime default)")
	}
	if autoscaleMinChanged && f.autoscaleMin < 0 {
		return fmt.Errorf("--autoscale-min must be >= 0")
	}
	if autoscaleMaxChanged && f.autoscaleMax < 0 {
		return fmt.Errorf("--autoscale-max must be >= 0")
	}

	// --tier and --replicas both set the pool size/shape, so only one may be
	// given. Parse the repeatable name=count specs into a placement map the
	// server accepts directly.
	var placement map[string]int
	if tierChanged {
		if replicasChanged {
			return fmt.Errorf("--tier and --replicas are mutually exclusive")
		}
		placement = make(map[string]int, len(f.tiers))
		for _, spec := range f.tiers {
			name, countStr, ok := strings.Cut(spec, "=")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				return fmt.Errorf("--tier must be name=count, got %q", spec)
			}
			count, err := strconv.Atoi(strings.TrimSpace(countStr))
			if err != nil || count < 0 {
				return fmt.Errorf("--tier %q count must be a non-negative integer", spec)
			}
			placement[name] = count
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

	// A replica change restarts the app and drops active sessions, so an
	// interactive caller must confirm first (mirrors the dashboard's guard).
	// The prompt is tty-gated: a non-interactive caller (CI, cron, piped
	// script) proceeds without prompting so automation that scales via the CLI
	// keeps working. --yes skips the prompt explicitly.
	if (replicasChanged || tierChanged) && !f.yes && isStdinTTY() {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Changing the replica pool restarts %q and drops active sessions. Continue? [y/N]: ", slug)
		var confirm string
		if _, err := fmt.Fscan(cmd.InOrStdin(), &confirm); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		switch strings.ToLower(confirm) {
		case "y", "yes":
			// proceed
		default:
			return fmt.Errorf("aborted - replica pool unchanged")
		}
	}

	payload := map[string]any{}

	if hibernateChanged {
		// -1 → send null (reset to global default); 0+ → send the value.
		var minutes *int
		if f.hibernateTimeout >= 0 {
			m := f.hibernateTimeout
			minutes = &m
		}
		payload["hibernate_timeout_minutes"] = minutes
	}
	if replicasChanged {
		payload["replicas"] = f.replicas
	}
	if tierChanged {
		payload["placement"] = placement
	}
	if capChanged {
		payload["max_sessions_per_replica"] = f.maxSessionsPerReplica
	}
	if minWarmReplicasChanged {
		payload["min_warm_replicas"] = f.minWarmReplicas
	}
	if anyAutoscaleChanged {
		// Send only the fields the caller changed; the server merges them over
		// the stored values so each field can be updated independently.
		as := map[string]any{}
		if autoscaleChanged {
			as["enabled"] = f.autoscale
		}
		if autoscaleMinChanged {
			as["min_replicas"] = f.autoscaleMin
		}
		if autoscaleMaxChanged {
			as["max_replicas"] = f.autoscaleMax
		}
		if autoscaleTargetChanged {
			as["target"] = f.autoscaleTarget
		}
		payload["autoscale"] = as
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PATCH", cfg.Host+"/api/apps/"+slug, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "update app", resp, out)
	}

	if hibernateChanged {
		minutes, _ := payload["hibernate_timeout_minutes"].(*int)
		switch {
		case minutes == nil:
			fmt.Printf("%s: hibernate-timeout reset to global default\n", slug)
		case *minutes == 0:
			fmt.Printf("%s: hibernation disabled\n", slug)
		default:
			fmt.Printf("%s: hibernate-timeout set to %d minutes\n", slug, *minutes)
		}
	}
	if replicasChanged {
		fmt.Printf("%s: replicas set to %d\n", slug, f.replicas)
	}
	if tierChanged {
		parts := make([]string, 0, len(f.tiers))
		total := 0
		for _, name := range sortedTierNames(placement) {
			parts = append(parts, fmt.Sprintf("%s=%d", name, placement[name]))
			total += placement[name]
		}
		fmt.Printf("%s: placement set to %s (%d replicas)\n", slug, strings.Join(parts, ", "), total)
	}
	if capChanged {
		if f.maxSessionsPerReplica == 0 {
			fmt.Printf("%s: max-sessions-per-replica reset to runtime default\n", slug)
		} else {
			fmt.Printf("%s: max-sessions-per-replica set to %d\n", slug, f.maxSessionsPerReplica)
		}
	}
	if minWarmReplicasChanged {
		fmt.Printf("%s: min-warm-replicas set to %d\n", slug, f.minWarmReplicas)
	}
	if anyAutoscaleChanged {
		if autoscaleChanged {
			if f.autoscale {
				fmt.Printf("%s: autoscale enabled\n", slug)
			} else {
				fmt.Printf("%s: autoscale disabled\n", slug)
			}
		}
		if autoscaleMinChanged {
			fmt.Printf("%s: autoscale-min set to %d\n", slug, f.autoscaleMin)
		}
		if autoscaleMaxChanged {
			fmt.Printf("%s: autoscale-max set to %d\n", slug, f.autoscaleMax)
		}
		if autoscaleTargetChanged {
			if f.autoscaleTarget == 0 {
				fmt.Printf("%s: autoscale-target reset to runtime default\n", slug)
			} else {
				fmt.Printf("%s: autoscale-target set to %.0f%%\n", slug, f.autoscaleTarget*100)
			}
		}
	}

	if f.wait {
		return waitForHealthyWithOutput(cfg, slug, f.waitTimeout, cmd.ErrOrStderr())
	}
	return nil
}

// sortedTierNames returns the placement map's tier names in alphabetical order
// so the confirmation output is deterministic.
func sortedTierNames(placement map[string]int) []string {
	names := make([]string, 0, len(placement))
	for name := range placement {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ── apps access ─────────────────────────────────────────────────────────────

func newAppsAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage app access control",
	}
	cmd.AddCommand(
		newAppsAccessSetCmd(),
		newAppsAccessGrantCmd(),
		newAppsAccessRevokeCmd(),
		newAppsAccessListCmd(),
		newAppsAccessGroupGrantCmd(),
		newAppsAccessGroupRevokeCmd(),
		newAppsAccessGroupListCmd(),
	)
	return cmd
}

func newAppsAccessSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <slug> <public|private|shared>",
		Short: "Set access level for an app",
		Args:  cobra.ExactArgs(2),
		RunE:  runAppsAccessSet,
	}
}

func runAppsAccessSet(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, accessLevel := args[0], args[1]
	body, err := json.Marshal(map[string]string{"access": accessLevel})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PATCH", cfg.Host+"/api/apps/"+slug+"/access", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "set access", resp, out)
	}
	fmt.Printf("%s: access set to %s\n", slug, accessLevel)
	return nil
}

type appsAccessGrantFlags struct {
	role string
}

func newAppsAccessGrantCmd() *cobra.Command {
	f := &appsAccessGrantFlags{}
	cmd := &cobra.Command{
		Use:   "grant <slug> <username>",
		Short: "Grant a user access to an app",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGrant(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.role, "role", "viewer", "Member role: viewer or manager")
	return cmd
}

func runAppsAccessGrant(cmd *cobra.Command, args []string, f *appsAccessGrantFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, username := args[0], args[1]
	payload := map[string]string{"username": username}
	// Only send role when explicitly set, so a plain `grant` never changes an
	// existing member's role (the server preserves it when role is absent).
	if cmd.Flags().Changed("role") {
		payload["role"] = f.role
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/members", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "grant access", resp, out)
	}
	if cmd.Flags().Changed("role") {
		fmt.Printf("%s: granted %s access to %s\n", slug, f.role, username)
	} else {
		fmt.Printf("%s: granted access to %s\n", slug, username)
	}
	return nil
}

func newAppsAccessRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <slug> <username>",
		Short: "Revoke a user's access to an app",
		Args:  cobra.ExactArgs(2),
		RunE:  runAppsAccessRevoke,
	}
}

func runAppsAccessRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, username := args[0], args[1]
	body, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug+"/members", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "revoke access", resp, out)
	}
	fmt.Printf("%s: revoked access for %s\n", slug, username)
	return nil
}

type appsAccessListFlags struct {
	jsonOutput bool
}

func newAppsAccessListCmd() *cobra.Command {
	f := &appsAccessListFlags{}
	cmd := &cobra.Command{
		Use:   "list <slug>",
		Short: "List members granted access to an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessList(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsAccessList(cmd *cobra.Command, args []string, f *appsAccessListFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/members", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "list members", resp, out)
	}
	if f.jsonOutput {
		fmt.Println(string(out))
		return nil
	}
	var members []struct {
		UserID   int64  `json:"user_id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(out, &members); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if len(members) == 0 {
		fmt.Printf("%s: no members\n", slug)
		return nil
	}
	for _, m := range members {
		fmt.Printf("%-20s %s\n", m.Username, m.Role)
	}
	return nil
}

// ── apps access group-grant / group-revoke / group-list ─────────────────────

type appsAccessGroupGrantFlags struct {
	role string
}

func newAppsAccessGroupGrantCmd() *cobra.Command {
	f := &appsAccessGroupGrantFlags{}
	cmd := &cobra.Command{
		Use:   "group-grant <slug> <group>",
		Short: "Grant an IdP group access to an app",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGroupGrant(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.role, "role", "viewer", "Group role: viewer or manager")
	return cmd
}

func runAppsAccessGroupGrant(cmd *cobra.Command, args []string, f *appsAccessGroupGrantFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, group := args[0], args[1]
	body, err := json.Marshal(map[string]string{"group": group, "role": f.role})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/group-access", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "grant group access", resp, out)
	}
	if warn := resp.Header.Get("X-ShinyHub-Warning"); warn != "" {
		fmt.Printf("warning: %s\n", warn)
	}
	fmt.Printf("%s: granted %s access to group %s\n", slug, f.role, group)
	return nil
}

func newAppsAccessGroupRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "group-revoke <slug> <group>",
		Short: "Revoke an IdP group's access to an app",
		Args:  cobra.ExactArgs(2),
		RunE:  runAppsAccessGroupRevoke,
	}
}

func runAppsAccessGroupRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, group := args[0], args[1]
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug+"/group-access/"+group, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "revoke group access", resp, out)
	}
	fmt.Printf("%s: revoked access for group %s\n", slug, group)
	return nil
}

type appsAccessGroupListFlags struct {
	jsonOutput bool
}

func newAppsAccessGroupListCmd() *cobra.Command {
	f := &appsAccessGroupListFlags{}
	cmd := &cobra.Command{
		Use:   "group-list <slug>",
		Short: "List IdP group access rules for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGroupList(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsAccessGroupList(cmd *cobra.Command, args []string, f *appsAccessGroupListFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/group-access", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "list group access", resp, out)
	}
	if f.jsonOutput {
		fmt.Println(string(out))
		return nil
	}
	var rules []struct {
		Group string `json:"group"`
		Role  string `json:"role"`
	}
	if err := json.Unmarshal(out, &rules); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if len(rules) == 0 {
		fmt.Printf("%s: no group rules\n", slug)
		return nil
	}
	for _, r := range rules {
		fmt.Printf("%-20s %s\n", r.Group, r.Role)
	}
	return nil
}

// ── tokens create ───────────────────────────────────────────────────────────

type tokensCreateFlags struct {
	name   string
	format string
}

func newTokensCreateCmd() *cobra.Command {
	f := &tokensCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokensCreate(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "Name for the token (required)")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().StringVar(&f.format, "format", "text", "Output format: text or json")
	return cmd
}

// tokenCreateResult holds the fields returned by the server on token creation.
type tokenCreateResult struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

func runTokensCreate(cmd *cobra.Command, args []string, f *tokensCreateFlags) error {
	switch f.format {
	case "text", "json":
		// valid
	default:
		return fmt.Errorf("--format must be %q or %q, got %q", "text", "json", f.format)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"name": f.name})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "create token", resp, out)
	}
	var result tokenCreateResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if f.format == "json" {
		out, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "API token: %s\n", result.Token)
	fmt.Fprintln(cmd.OutOrStdout(), "Store this - it will not be shown again.")
	_ = os.Stdout.Sync()
	return nil
}

// ── apps delete ─────────────────────────────────────────────────────────────

type appsDeleteFlags struct {
	yes bool
}

func newAppsDeleteCmd() *cobra.Command {
	f := &appsDeleteFlags{}
	cmd := &cobra.Command{
		Use:   "delete <slug>",
		Short: "Permanently delete an app and all its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDelete(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func runAppsDelete(cmd *cobra.Command, args []string, f *appsDeleteFlags) error {
	slug := args[0]

	if !f.yes {
		// Without --yes the destructive `apps delete` flow REQUIRES a
		// confirmation. When stdin isn't a tty (CI, cron, `< /dev/null`,
		// piped scripts) the previous code blocked forever on the read or
		// surfaced a confusing "read confirmation: EOF". Refuse fast with
		// a message that points at --yes so automation has a clear path.
		if !isStdinTTY() {
			return fmt.Errorf("apps delete requires interactive confirmation; pass --yes to skip the prompt for non-interactive use")
		}
		// Prompt goes to stderr so a `shinyhub apps delete foo | tee log`
		// pipeline keeps stdout for the success line only.
		fmt.Fprintf(cmd.ErrOrStderr(), "This will permanently delete app %q and all its data. Type the slug to confirm: ", slug)
		var confirm string
		if _, err := fmt.Fscan(cmd.InOrStdin(), &confirm); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if confirm != slug {
			return fmt.Errorf("confirmation did not match slug %q - aborted", slug)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "delete app", resp, out)
	}
	fmt.Printf("%s: deleted\n", slug)
	return nil
}

// ── apps stop ───────────────────────────────────────────────────────────────

func newAppsStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <slug>",
		Short: "Stop a running app",
		Args:  cobra.ExactArgs(1),
		RunE:  runAppsStop,
	}
}

func runAppsStop(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/stop", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "stop app", resp, out)
	}
	fmt.Printf("%s: stopped\n", slug)
	return nil
}

// ── apps deployments ────────────────────────────────────────────────────────

type appsDeploymentsFlags struct {
	jsonOutput bool
}

func newAppsDeploymentsCmd() *cobra.Command {
	f := &appsDeploymentsFlags{}
	cmd := &cobra.Command{
		Use:   "deployments <slug>",
		Short: "List deployment history for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDeployments(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsDeployments(cmd *cobra.Command, args []string, f *appsDeploymentsFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/deployments", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "list deployments", resp, out)
	}

	if f.jsonOutput {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var deployments []struct {
		ID        int64  `json:"id"`
		Version   string `json:"version"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(out, &deployments); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(deployments) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No deployments.")
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-6s %-20s %-12s %s\n", "ID", "VERSION", "STATUS", "CREATED")
	for _, d := range deployments {
		created := d.CreatedAt
		if len(created) > 19 {
			created = created[:19]
		}
		row := fmt.Sprintf("%-6d %-20s %-12s %s", d.ID, d.Version, d.Status, created)
		fmt.Fprintln(w, strings.TrimRight(row, " "))
	}
	return nil
}

// ── tokens list ─────────────────────────────────────────────────────────────

// tokenInfo is a safe view of an API key, matching the server's list response.
type tokenInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// fetchTokens retrieves all tokens for the authenticated user.
func fetchTokens(cfg *cliConfig) ([]tokenInfo, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/tokens", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "list tokens", resp, out)
	}
	var tokens []tokenInfo
	if err := json.Unmarshal(out, &tokens); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return tokens, nil
}

type tokensListFlags struct {
	jsonOutput bool
}

func newTokensListCmd() *cobra.Command {
	f := &tokensListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your API tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokensList(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runTokensList(cmd *cobra.Command, args []string, f *tokensListFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if f.jsonOutput {
		req, err := http.NewRequest("GET", cfg.Host+"/api/tokens", nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "list tokens", resp, out)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	tokens, err := fetchTokens(cfg)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No tokens.")
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-6s %-24s %s\n", "ID", "NAME", "CREATED")
	for _, t := range tokens {
		created := t.CreatedAt
		if len(created) > 19 {
			created = created[:19]
		}
		row := fmt.Sprintf("%-6d %-24s %s", t.ID, t.Name, created)
		fmt.Fprintln(w, strings.TrimRight(row, " "))
	}
	return nil
}

// ── tokens revoke ───────────────────────────────────────────────────────────

type tokensRevokeFlags struct {
	name string
}

func newTokensRevokeCmd() *cobra.Command {
	f := &tokensRevokeFlags{}
	cmd := &cobra.Command{
		Use:   "revoke [<id>]",
		Short: "Revoke an API token by ID or name",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokensRevoke(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "Revoke the token with this name")
	return cmd
}

func runTokensRevoke(cmd *cobra.Command, args []string, f *tokensRevokeFlags) error {
	hasID := len(args) == 1
	hasName := f.name != ""

	if hasID && hasName {
		return fmt.Errorf("specify either id or --name, not both")
	}
	if !hasID && !hasName {
		return fmt.Errorf("provide a token id or --name")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var tokenID string
	if hasID {
		tokenID = args[0]
	} else {
		tokens, err := fetchTokens(cfg)
		if err != nil {
			return err
		}
		var matches []tokenInfo
		for _, t := range tokens {
			if t.Name == f.name {
				matches = append(matches, t)
			}
		}
		switch len(matches) {
		case 0:
			return fmt.Errorf("no token named %q", f.name)
		case 1:
			tokenID = fmt.Sprintf("%d", matches[0].ID)
		default:
			return fmt.Errorf("multiple tokens named %q; revoke by id instead", f.name)
		}
	}

	req, err := http.NewRequest("DELETE", cfg.Host+"/api/tokens/"+tokenID, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "revoke token", resp, out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "token %s: revoked\n", tokenID)
	return nil
}
