package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var appsCmd = &cobra.Command{Use: "apps", Short: "Manage apps"}
var tokensCmd = &cobra.Command{Use: "tokens", Short: "Manage API tokens"}

func init() {
	appsCmd.AddCommand(appsListCmd, appsLogsCmd, appsRollbackCmd, appsRestartCmd, appsSetCmd)
	tokensCmd.AddCommand(tokensCreateCmd)
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
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	return scanner.Err()
}

var appsRollbackCmd = &cobra.Command{
	Use:  "rollback <slug>",
	Args: cobra.ExactArgs(1),
	RunE: rollbackOrRestart("rollback", "PUT"),
}

var appsRestartCmd = &cobra.Command{
	Use:  "restart <slug>",
	Args: cobra.ExactArgs(1),
	RunE: rollbackOrRestart("restart", "POST"),
}

var appsSetFlags struct {
	hibernateTimeout int
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
}

func runAppsSet(cmd *cobra.Command, args []string) error {
	if !cmd.Flags().Changed("hibernate-timeout") {
		return fmt.Errorf("at least one flag is required (e.g. --hibernate-timeout)")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

	// -1 → send null (reset to global default); 0+ → send the value.
	var minutes *int
	if appsSetFlags.hibernateTimeout >= 0 {
		m := appsSetFlags.hibernateTimeout
		minutes = &m
	}

	body, err := json.Marshal(map[string]any{"hibernate_timeout_minutes": minutes})
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

	switch {
	case minutes == nil:
		fmt.Printf("%s: hibernate-timeout reset to global default\n", slug)
	case *minutes == 0:
		fmt.Printf("%s: hibernation disabled\n", slug)
	default:
		fmt.Printf("%s: hibernate-timeout set to %d minutes\n", slug, *minutes)
	}
	return nil
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
