package cli

import (
	"bufio"
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
	appsCmd.AddCommand(appsListCmd, appsLogsCmd, appsRollbackCmd, appsRestartCmd)
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
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var apps []map[string]any
	json.NewDecoder(resp.Body).Decode(&apps)
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
	Short: "Tail live logs for an app",
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
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
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
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
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

var tokensCreateCmd = &cobra.Command{
	Use:  "create",
	RunE: runTokensCreate,
}

func runTokensCreate(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/tokens", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if key, ok := result["key"]; ok {
		fmt.Printf("API key: %s\n", key)
		fmt.Println("Store this — it will not be shown again.")
	}
	_ = os.Stdout.Sync()
	return nil
}
