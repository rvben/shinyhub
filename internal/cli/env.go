package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var envKeyRegex = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// newEnvCmd builds a fresh env command tree each time it is called.
// Using a factory avoids flag-state leakage between tests when the package-level
// cobra command variables are reused across multiple cmd.Execute() calls.
func newEnvCmd() *cobra.Command {
	envCmd := &cobra.Command{Use: "env", Short: "Manage app environment variables"}

	var setFlags struct {
		secret  bool
		stdin   bool
		restart bool
	}

	envSetCmd := &cobra.Command{
		Use:   "set <slug> KEY=VALUE|KEY",
		Short: "Set an environment variable for an app",
		Args:  cobra.ExactArgs(2),
	}
	envSetCmd.Flags().BoolVar(&setFlags.secret, "secret", false, "Mark value as a secret (encrypted at rest, write-only)")
	envSetCmd.Flags().BoolVar(&setFlags.stdin, "stdin", false, "Read value from stdin (bare KEY arg required)")
	envSetCmd.Flags().BoolVar(&setFlags.restart, "restart", false, "Restart the app after saving")
	envSetCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		keyArg := args[1]

		var key, value string
		if idx := strings.IndexByte(keyArg, '='); idx >= 0 {
			key = keyArg[:idx]
			value = keyArg[idx+1:]
		} else {
			if !setFlags.stdin {
				return fmt.Errorf("provide KEY=VALUE or KEY with --stdin")
			}
			key = keyArg
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			value = strings.TrimRight(string(raw), "\r\n")
		}

		if !envKeyRegex.MatchString(key) {
			return fmt.Errorf("invalid env key %q: must match ^[A-Z_][A-Z0-9_]*$", key)
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		body, err := json.Marshal(map[string]any{"value": value, "secret": setFlags.secret})
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}

		rawURL := cfg.Host + "/api/apps/" + slug + "/env/" + key
		if setFlags.restart {
			rawURL += "?restart=true"
		}

		req, err := http.NewRequest("PUT", rawURL, bytes.NewReader(body))
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
			return fmt.Errorf("server returned %s: %s", resp.Status, out)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s: set %s\n", slug, key)
		return nil
	}

	var lsFlags struct {
		jsonOutput bool
	}

	envLsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List environment variables for an app",
		Args:  cobra.ExactArgs(1),
	}
	envLsCmd.Flags().BoolVar(&lsFlags.jsonOutput, "json", false, "Output as JSON")
	envLsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/env", nil)
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
			return fmt.Errorf("server returned %s: %s", resp.Status, out)
		}

		if lsFlags.jsonOutput {
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}

		var result struct {
			Env []struct {
				Key       string `json:"key"`
				Value     string `json:"value"`
				Secret    bool   `json:"secret"`
				Set       bool   `json:"set"`
				UpdatedAt int64  `json:"updated_at"`
			} `json:"env"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "%-24s %s\n", "KEY", "VALUE")
		for _, v := range result.Env {
			displayValue := v.Value
			switch {
			case v.Secret:
				displayValue = "••••••"
			case v.Set && v.Value == "":
				displayValue = "(empty)"
			}
			row := fmt.Sprintf("%-24s %s", v.Key, displayValue)
			fmt.Fprintln(w, strings.TrimRight(row, " "))
		}
		return nil
	}

	var rmFlags struct {
		restart bool
	}

	envRmCmd := &cobra.Command{
		Use:   "rm <slug> KEY",
		Short: "Remove an environment variable from an app",
		Args:  cobra.ExactArgs(2),
	}
	envRmCmd.Flags().BoolVar(&rmFlags.restart, "restart", false, "Restart the app after deleting")
	envRmCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		key := args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		rawURL := cfg.Host + "/api/apps/" + slug + "/env/" + key
		if rmFlags.restart {
			rawURL += "?restart=true"
		}

		req, err := http.NewRequest("DELETE", rawURL, nil)
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

		fmt.Fprintf(cmd.OutOrStdout(), "%s: removed %s\n", slug, key)
		return nil
	}

	envCmd.AddCommand(envSetCmd, envLsCmd, envRmCmd)
	return envCmd
}

// envCmd is the package-level command registered with the root command.
var envCmd = newEnvCmd()
