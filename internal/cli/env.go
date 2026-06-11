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
			return fmt.Errorf("env var keys must be uppercase letters, digits and underscores (e.g. FOO_BAR); %q is invalid", key)
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		bodyBytes, err := json.Marshal(map[string]any{"value": value, "secret": setFlags.secret})
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}

		baseURL := cfg.Host + "/api/apps/" + slug + "/env/" + key

		// First request: write the value without triggering a restart yet.
		// We read the response to learn whether the value actually changed before
		// deciding whether to restart; sending restart=true on an unchanged value
		// would needlessly cycle the app.
		req, err := http.NewRequest("PUT", baseURL, bytes.NewReader(bodyBytes))
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

		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "set env", resp, respBody)
		}

		// Decode the response to check whether the value was actually new.
		var result map[string]any
		changed := true // default to changed if we cannot parse the response
		if json.Unmarshal(respBody, &result) == nil {
			if v, ok := result["changed"].(bool); ok {
				changed = v
			}
		}

		if !changed {
			// Value was already set to this exact value; no restart needed.
			return renderAction(cmd, "unchanged",
				map[string]any{"slug": slug, "key": key},
				fmt.Sprintf("%s: %s unchanged", slug, key))
		}

		// Value changed. If the caller requested a restart, re-send with
		// ?restart=true so the app picks up the new value immediately.
		if setFlags.restart {
			req2, err := http.NewRequest("PUT", baseURL+"?restart=true", bytes.NewReader(bodyBytes))
			if err != nil {
				return fmt.Errorf("build restart request: %w", err)
			}
			req2.Header.Set("Authorization", authHeader(cfg.Token))
			req2.Header.Set("Content-Type", "application/json")
			resp2, err := httpClient.Do(req2)
			if err != nil {
				return fmt.Errorf("restart after env set: %w", err)
			}
			defer resp2.Body.Close()
			// Non-fatal: the value is already saved; ignore restart errors here.
		}

		return renderAction(cmd, "set",
			map[string]any{"slug": slug, "key": key},
			fmt.Sprintf("%s: set %s", slug, key))
	}

	lsF := &listFlags{}

	envLsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List environment variables for an app",
		Args:  cobra.ExactArgs(1),
	}
	addListFlags(envLsCmd, lsF)
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
			return httpError(cfg.Token, "list env", resp, out)
		}

		// The server wraps env entries under {"env": [...]}.
		var result struct {
			Env []map[string]any `json:"env"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return renderList(cmd, lsF, result.Env, nil, func(w io.Writer, items []map[string]any) {
			fmt.Fprintf(w, "%-24s %s\n", "KEY", "VALUE")
			for _, v := range items {
				key := fmt.Sprintf("%v", v["key"])
				value := fmt.Sprintf("%v", v["value"])
				secret, _ := v["secret"].(bool)
				set, _ := v["set"].(bool)
				displayValue := value
				switch {
				case secret:
					displayValue = "••••••"
				case set && value == "":
					displayValue = "(empty)"
				}
				row := fmt.Sprintf("%-24s %s", key, displayValue)
				fmt.Fprintln(w, strings.TrimRight(row, " "))
			}
		})
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
			return httpError(cfg.Token, "remove env", resp, out)
		}

		return renderAction(cmd, "removed",
			map[string]any{"slug": slug, "key": key},
			fmt.Sprintf("%s: removed %s", slug, key))
	}

	envCmd.AddCommand(envSetCmd, envLsCmd, envRmCmd, newEnvApplyCmd())
	return envCmd
}
