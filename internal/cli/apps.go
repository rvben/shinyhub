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

var appsCmd = &cobra.Command{Use: "apps", Short: "Manage apps"}
var tokensCmd = &cobra.Command{Use: "tokens", Short: "Manage API tokens"}

func init() {
	appsCmd.AddCommand(appsListCmd, appsLogsCmd, appsRollbackCmd, appsRestartCmd, appsSetCmd, appsAccessCmd, appsDeleteCmd, appsStopCmd, appsDeploymentsCmd)
	tokensCmd.AddCommand(tokensCreateCmd, tokensListCmd, tokensRevokeCmd)
}

var appsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all apps",
	RunE:  runAppsList,
}

func runAppsList(cmd *cobra.Command, args []string) error {
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
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}
	var apps []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(apps) == 0 {
		fmt.Println("No apps.")
		return nil
	}
	fmt.Printf("%-20s %-10s %-12s\n", "SLUG", "STATUS", "DEPLOYS")
	for _, a := range apps {
		fmt.Printf("%-20s %-10s %-12v\n", a["slug"], a["status"], a["deploy_count"])
	}
	return nil
}

var appsLogsCmd = &cobra.Command{
	Use:   "logs <slug>",
	Short: "Connect to the SSE log stream for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsLogs,
}

func runAppsLogs(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+args[0]+"/logs", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Accept", "text/event-stream")
	// Use http.DefaultClient for SSE streaming — no timeout, connection is indefinite.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	return scanner.Err()
}

var rollbackFlags struct {
	deploymentID int64
}

var appsRollbackCmd = &cobra.Command{
	Use:   "rollback <slug>",
	Short: "Roll back an app to the previous or a specific historical deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsRollback,
}

func init() {
	appsRollbackCmd.Flags().Int64Var(&rollbackFlags.deploymentID, "to", 0,
		"Deployment ID to roll back to (default: previous deployment)")
}

func runAppsRollback(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

	var bodyReader io.Reader
	if cmd.Flags().Changed("to") {
		body, err := json.Marshal(map[string]any{"deployment_id": rollbackFlags.deploymentID})
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
		return fmt.Errorf("rollback failed: %s", out)
	}
	fmt.Printf("rollback: %s\n", out)
	return nil
}

var appsRestartCmd = &cobra.Command{
	Use:  "restart <slug>",
	Args: cobra.ExactArgs(1),
	RunE: rollbackOrRestart("restart", "POST"),
}

var appsSetFlags struct {
	hibernateTimeout      int
	replicas              int
	maxSessionsPerReplica int
}

var appsSetCmd = &cobra.Command{
	Use:   "set <slug>",
	Short: "Update app settings",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsSet,
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
			return fmt.Errorf("%s failed: %s", action, out)
		}
		fmt.Printf("%s: %s\n", action, out)
		return nil
	}
}

func init() {
	appsSetCmd.Flags().IntVar(&appsSetFlags.hibernateTimeout, "hibernate-timeout", 0,
		"Idle timeout minutes before hibernation (-1 = reset to global default, 0 = disable, N = N minutes)")
	appsSetCmd.Flags().IntVar(&appsSetFlags.replicas, "replicas", 0,
		"Number of replica processes serving this app (>= 1)")
	appsSetCmd.Flags().IntVar(&appsSetFlags.maxSessionsPerReplica, "max-sessions-per-replica", -1,
		"Per-replica new-session admission cap (0 = runtime default; 1..1000 = explicit)")
}

func runAppsSet(cmd *cobra.Command, args []string) error {
	hibernateChanged := cmd.Flags().Changed("hibernate-timeout")
	replicasChanged := cmd.Flags().Changed("replicas")
	capChanged := cmd.Flags().Changed("max-sessions-per-replica")

	// -1 is the flag's default sentinel; if the user explicitly passes -1 it
	// means "I didn't really mean to set this", so treat it as not provided.
	if capChanged && appsSetFlags.maxSessionsPerReplica == -1 {
		capChanged = false
	}

	if !hibernateChanged && !replicasChanged && !capChanged {
		return fmt.Errorf("at least one flag is required (e.g. --hibernate-timeout, --replicas, --max-sessions-per-replica)")
	}
	if replicasChanged && appsSetFlags.replicas < 1 {
		return fmt.Errorf("--replicas must be >= 1")
	}
	if capChanged && (appsSetFlags.maxSessionsPerReplica < 0 || appsSetFlags.maxSessionsPerReplica > 1000) {
		return fmt.Errorf("--max-sessions-per-replica must be between 0 and 1000")
	}
	if hibernateChanged && appsSetFlags.hibernateTimeout < -1 {
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
		if appsSetFlags.hibernateTimeout >= 0 {
			m := appsSetFlags.hibernateTimeout
			minutes = &m
		}
		payload["hibernate_timeout_minutes"] = minutes
	}
	if replicasChanged {
		payload["replicas"] = appsSetFlags.replicas
	}
	if capChanged {
		payload["max_sessions_per_replica"] = appsSetFlags.maxSessionsPerReplica
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
		return fmt.Errorf("set failed (%s): %s", resp.Status, out)
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
		fmt.Printf("%s: replicas set to %d\n", slug, appsSetFlags.replicas)
	}
	if capChanged {
		if appsSetFlags.maxSessionsPerReplica == 0 {
			fmt.Printf("%s: max-sessions-per-replica reset to runtime default\n", slug)
		} else {
			fmt.Printf("%s: max-sessions-per-replica set to %d\n", slug, appsSetFlags.maxSessionsPerReplica)
		}
	}
	return nil
}

var appsAccessCmd = &cobra.Command{
	Use:   "access",
	Short: "Manage app access control",
}

var appsAccessSetCmd = &cobra.Command{
	Use:   "set <slug> <public|private|shared>",
	Short: "Set access level for an app",
	Args:  cobra.ExactArgs(2),
	RunE:  runAppsAccessSet,
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
		return fmt.Errorf("set access failed: %s", out)
	}
	fmt.Printf("%s: access set to %s\n", slug, accessLevel)
	return nil
}

func init() {
	appsAccessCmd.AddCommand(appsAccessSetCmd)
}

var tokenName string

var tokensCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new API token",
	RunE:  runTokensCreate,
}

func init() {
	tokensCreateCmd.Flags().StringVar(&tokenName, "name", "", "Name for the token (required)")
	tokensCreateCmd.MarkFlagRequired("name")
}

func runTokensCreate(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"name": tokenName})
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
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if token, ok := result["token"]; ok {
		fmt.Printf("API token: %s\n", token)
		fmt.Println("Store this — it will not be shown again.")
	}
	_ = os.Stdout.Sync()
	return nil
}

// ── apps delete ─────────────────────────────────────────────────────────────

var appsDeleteFlags struct {
	yes bool
}

var appsDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Permanently delete an app and all its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsDelete,
}

func init() {
	appsDeleteCmd.Flags().BoolVar(&appsDeleteFlags.yes, "yes", false, "Skip confirmation prompt")
}

func runAppsDelete(cmd *cobra.Command, args []string) error {
	slug := args[0]

	if !appsDeleteFlags.yes {
		fmt.Printf("This will permanently delete app %q and all its data. Type the slug to confirm: ", slug)
		var confirm string
		if _, err := fmt.Fscan(cmd.InOrStdin(), &confirm); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if confirm != slug {
			return fmt.Errorf("confirmation did not match slug %q — aborted", slug)
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
		return fmt.Errorf("delete failed (%s): %s", resp.Status, strings.TrimSpace(string(out)))
	}
	fmt.Printf("%s: deleted\n", slug)
	return nil
}

// ── apps stop ───────────────────────────────────────────────────────────────

var appsStopCmd = &cobra.Command{
	Use:   "stop <slug>",
	Short: "Stop a running app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsStop,
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
		return fmt.Errorf("stop failed (%s): %s", resp.Status, strings.TrimSpace(string(out)))
	}
	fmt.Printf("%s: stopped\n", slug)
	return nil
}

// ── apps deployments ────────────────────────────────────────────────────────

var appsDeploymentsFlags struct {
	jsonOutput bool
}

var appsDeploymentsCmd = &cobra.Command{
	Use:   "deployments <slug>",
	Short: "List deployment history for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsDeployments,
}

func init() {
	appsDeploymentsCmd.Flags().BoolVar(&appsDeploymentsFlags.jsonOutput, "json", false, "Output as JSON")
}

func runAppsDeployments(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}

	if appsDeploymentsFlags.jsonOutput {
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

var tokensListFlags struct {
	jsonOutput bool
}

var tokensListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your API tokens",
	RunE:  runTokensList,
}

func init() {
	tokensListCmd.Flags().BoolVar(&tokensListFlags.jsonOutput, "json", false, "Output as JSON")
}

func runTokensList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
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
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}

	if tokensListFlags.jsonOutput {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var tokens []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(out, &tokens); err != nil {
		return fmt.Errorf("decode response: %w", err)
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

var tokensRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an API token by ID",
	Args:  cobra.ExactArgs(1),
	RunE:  runTokensRevoke,
}

func runTokensRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	id := args[0]
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/tokens/"+id, nil)
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
		return fmt.Errorf("revoke failed (%s): %s", resp.Status, strings.TrimSpace(string(out)))
	}
	fmt.Printf("token %s: revoked\n", id)
	return nil
}
