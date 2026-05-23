package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
		return fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
	}

	if f.jsonOutput {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var resp2 struct {
		App struct {
			Slug                  string `json:"slug"`
			Name                  string `json:"name"`
			OwnerID               int64  `json:"owner_id"`
			Access                string `json:"access"`
			Status                string `json:"status"`
			Replicas              int    `json:"replicas"`
			MaxSessionsPerReplica int    `json:"max_sessions_per_replica"`
			DeployCount           int    `json:"deploy_count"`
			HibernateTimeoutMinutes *int   `json:"hibernate_timeout_minutes"`
			MemoryLimitMB           *int   `json:"memory_limit_mb"`
			CPUQuotaPercent         *int   `json:"cpu_quota_percent"`
			ProjectSlug             string `json:"project_slug,omitempty"`
			CreatedAt               string `json:"created_at"`
			UpdatedAt               string `json:"updated_at"`
		} `json:"app"`
		ReplicasStatus []struct {
			Index  int    `json:"index"`
			Status string `json:"status"`
			PID    *int   `json:"pid"`
			Port   *int   `json:"port"`
		} `json:"replicas_status"`
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
	fmt.Fprintf(w, "Max sess/r:  %d\n", a.MaxSessionsPerReplica)
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
			fmt.Fprintf(w, "  %-6d %-10s %-8s %s\n", r.Index, r.Status, pid, port)
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
		return fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return fmt.Errorf("rollback failed: %s", unwrapServerError(out, "no error body"))
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
			return fmt.Errorf("start failed: %s", unwrapServerError(out, "no error body"))
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
			return fmt.Errorf("%s failed: %s", action, unwrapServerError(out, "no error body"))
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
	return cmd
}

func runAppsSet(cmd *cobra.Command, args []string, f *appsSetFlags) error {
	hibernateChanged := cmd.Flags().Changed("hibernate-timeout")
	replicasChanged := cmd.Flags().Changed("replicas")
	capChanged := cmd.Flags().Changed("max-sessions-per-replica")

	if !hibernateChanged && !replicasChanged && !capChanged {
		return fmt.Errorf("at least one flag is required (e.g. --hibernate-timeout, --replicas, --max-sessions-per-replica)")
	}
	if replicasChanged && f.replicas < 1 {
		return fmt.Errorf("--replicas must be >= 1")
	}
	if capChanged && (f.maxSessionsPerReplica < 0 || f.maxSessionsPerReplica > 1000) {
		return fmt.Errorf("--max-sessions-per-replica must be between 0 and 1000")
	}
	if hibernateChanged && f.hibernateTimeout < -1 {
		return fmt.Errorf("--hibernate-timeout must be -1 (reset to global default), 0 (disable), or a positive number of minutes")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

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
	if capChanged {
		payload["max_sessions_per_replica"] = f.maxSessionsPerReplica
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
		return fmt.Errorf("set failed (%s): %s", resp.Status, unwrapServerError(out, "no error body"))
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
	if capChanged {
		if f.maxSessionsPerReplica == 0 {
			fmt.Printf("%s: max-sessions-per-replica reset to runtime default\n", slug)
		} else {
			fmt.Printf("%s: max-sessions-per-replica set to %d\n", slug, f.maxSessionsPerReplica)
		}
	}
	return nil
}

// ── apps access ─────────────────────────────────────────────────────────────

func newAppsAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage app access control",
	}
	cmd.AddCommand(newAppsAccessSetCmd())
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
		return fmt.Errorf("set access failed: %s", unwrapServerError(out, "no error body"))
	}
	fmt.Printf("%s: access set to %s\n", slug, accessLevel)
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
		return fmt.Errorf("server returned %d", resp.StatusCode)
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
		return fmt.Errorf("delete failed (%s): %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return fmt.Errorf("stop failed (%s): %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
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
			return fmt.Errorf("server returned %s: %s", resp.Status, unwrapServerError(out, "no error body"))
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
		return fmt.Errorf("revoke failed (%s): %s", resp.Status, unwrapServerError(out, "no error body"))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "token %s: revoked\n", tokenID)
	return nil
}
