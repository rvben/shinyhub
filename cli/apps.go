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
	Use:  "list",
	RunE: runAppsList,
}

func runAppsList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("GET", cfg.Host+"/api/apps", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
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
		fmt.Printf("%-20s %-10s %-12v\n", a["Slug"], a["Status"], a["DeployCount"])
	}
	return nil
}

var appsLogsCmd = &cobra.Command{
	Use:  "logs <slug>",
	Args: cobra.ExactArgs(1),
	RunE: runAppsLogs,
}

func runAppsLogs(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("GET", cfg.Host+"/api/apps/"+args[0]+"/logs", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "text/event-stream")
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
		req, _ := http.NewRequest(method, cfg.Host+"/api/apps/"+slug+"/"+action, nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := http.DefaultClient.Do(req)
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
	req, _ := http.NewRequest("POST", cfg.Host+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
