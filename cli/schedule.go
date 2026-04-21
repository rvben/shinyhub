package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mattn/go-shellwords"
	"github.com/spf13/cobra"
)

// scheduleCmd is the package-level command registered with the root command.
var scheduleCmd = newScheduleCmd()

// newScheduleCmd builds a fresh schedule command tree each time it is called.
func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Manage scheduled jobs for an app"}
	cmd.AddCommand(
		newScheduleLsCmd(),
		newScheduleAddCmd(),
		newScheduleRmCmd(),
		newScheduleEnableCmd(),
		newScheduleDisableCmd(),
		newScheduleRunCmd(),
		newScheduleLogsCmd(),
	)
	return cmd
}

// scheduleDTO mirrors the server's JSON representation of a schedule.
type scheduleDTO struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	CronExpr       string   `json:"cron_expr"`
	Command        []string `json:"command"`
	Enabled        bool     `json:"enabled"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	OverlapPolicy  string   `json:"overlap_policy"`
	MissedPolicy   string   `json:"missed_policy"`
}

// lookupScheduleID resolves a schedule name to its numeric ID by listing all
// schedules for the app and returning the ID of the first match.
func lookupScheduleID(cfg *cliConfig, slug, name string) (int64, error) {
	schedules, err := listSchedules(cfg, slug)
	if err != nil {
		return 0, err
	}
	for _, s := range schedules {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return 0, fmt.Errorf("schedule %q not found for app %q", name, slug)
}

// listSchedules fetches all schedules for the given app slug.
func listSchedules(cfg *cliConfig, slug string) ([]scheduleDTO, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/schedules", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, out)
	}

	var schedules []scheduleDTO
	if err := json.NewDecoder(resp.Body).Decode(&schedules); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return schedules, nil
}

func newScheduleLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <slug>",
		Short: "List scheduled jobs for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			schedules, err := listSchedules(cfg, args[0])
			if err != nil {
				return err
			}
			if len(schedules) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No schedules.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-6s  %-24s  %-20s  %-8s  %s\n",
				"ID", "NAME", "CRON", "ENABLED", "COMMAND")
			for _, s := range schedules {
				cmdStr := strings.Join(s.Command, " ")
				enabled := "true"
				if !s.Enabled {
					enabled = "false"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-6d  %-24s  %-20s  %-8s  %s\n",
					s.ID, s.Name, s.CronExpr, enabled, cmdStr)
			}
			return nil
		},
	}
}

func newScheduleAddCmd() *cobra.Command {
	var flags struct {
		name     string
		cron     string
		cmd      string
		cmdJSON  string
		timeout  int
		overlap  string
		missed   string
		disabled bool
	}

	addCmd := &cobra.Command{
		Use:   "add <slug>",
		Short: "Add a scheduled job to an app",
		Args:  cobra.ExactArgs(1),
	}
	addCmd.Flags().StringVar(&flags.name, "name", "", "Schedule name (required)")
	addCmd.Flags().StringVar(&flags.cron, "cron", "", "Cron expression, e.g. '0 * * * *' (required)")
	addCmd.Flags().StringVar(&flags.cmd, "cmd", "", "Command as a shell string, e.g. 'python run.py --flag x'")
	addCmd.Flags().StringVar(&flags.cmdJSON, "cmd-json", "", `Command as a JSON array, e.g. '["python","run.py"]'`)
	addCmd.Flags().IntVar(&flags.timeout, "timeout", 3600, "Timeout in seconds (1..86400)")
	addCmd.Flags().StringVar(&flags.overlap, "overlap", "skip", "Overlap policy: skip|queue|concurrent")
	addCmd.Flags().StringVar(&flags.missed, "missed", "skip", "Missed-run policy: skip|run_once")
	addCmd.Flags().BoolVar(&flags.disabled, "disabled", false, "Create the schedule in disabled state")
	_ = addCmd.MarkFlagRequired("name")
	_ = addCmd.MarkFlagRequired("cron")

	addCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		var command []string
		switch {
		case flags.cmd != "" && flags.cmdJSON != "":
			return fmt.Errorf("specify exactly one of --cmd or --cmd-json")
		case flags.cmd != "":
			command, err = shellwords.Parse(flags.cmd)
			if err != nil {
				return fmt.Errorf("parse --cmd: %w", err)
			}
		case flags.cmdJSON != "":
			if err := json.Unmarshal([]byte(flags.cmdJSON), &command); err != nil {
				return fmt.Errorf("parse --cmd-json: %w", err)
			}
		default:
			return fmt.Errorf("one of --cmd or --cmd-json is required")
		}

		enabled := !flags.disabled
		payload := map[string]any{
			"name":            flags.name,
			"cron_expr":       flags.cron,
			"command":         command,
			"enabled":         enabled,
			"timeout_seconds": flags.timeout,
			"overlap_policy":  flags.overlap,
			"missed_policy":   flags.missed,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/schedules", bytes.NewReader(body))
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
			return fmt.Errorf("server returned %s: %s", resp.Status, out)
		}

		var created scheduleDTO
		if err := json.Unmarshal(out, &created); err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "created schedule %q (id %d)\n", created.Name, created.ID)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: schedule %q created\n", slug, flags.name)
		}
		return nil
	}
	return addCmd
}

func newScheduleRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <slug> <name>",
		Short: "Remove a scheduled job from an app",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, name := args[0], args[1]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			id, err := lookupScheduleID(cfg, slug, name)
			if err != nil {
				return err
			}

			url := fmt.Sprintf("%s/api/apps/%s/schedules/%d", cfg.Host, slug, id)
			req, err := http.NewRequest("DELETE", url, nil)
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

			fmt.Fprintf(cmd.OutOrStdout(), "%s: removed schedule %q\n", slug, name)
			return nil
		},
	}
}

func newScheduleEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <slug> <name>",
		Short: "Enable a scheduled job",
		Args:  cobra.ExactArgs(2),
		RunE:  patchScheduleEnabled(true),
	}
}

func newScheduleDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <slug> <name>",
		Short: "Disable a scheduled job",
		Args:  cobra.ExactArgs(2),
		RunE:  patchScheduleEnabled(false),
	}
}

func patchScheduleEnabled(enabled bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		slug, name := args[0], args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		id, err := lookupScheduleID(cfg, slug, name)
		if err != nil {
			return err
		}

		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		url := fmt.Sprintf("%s/api/apps/%s/schedules/%d", cfg.Host, slug, id)
		req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
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

		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: schedule %q %s\n", slug, name, state)
		return nil
	}
}

func newScheduleRunCmd() *cobra.Command {
	var follow bool

	runCmd := &cobra.Command{
		Use:   "run <slug> <name>",
		Short: "Trigger a scheduled job immediately",
		Args:  cobra.ExactArgs(2),
	}
	runCmd.Flags().BoolVar(&follow, "follow", false, "Stream logs after triggering")

	runCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug, name := args[0], args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		id, err := lookupScheduleID(cfg, slug, name)
		if err != nil {
			return err
		}

		url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/run", cfg.Host, slug, id)
		req, err := http.NewRequest("POST", url, nil)
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

		fmt.Fprintf(cmd.OutOrStdout(), "%s: schedule %q started\n", slug, name)

		if !follow {
			return nil
		}

		// Tail the latest run's logs by fetching the run list then streaming.
		runsURL := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs?limit=1", cfg.Host, slug, id)
		runsReq, err := http.NewRequest("GET", runsURL, nil)
		if err != nil {
			return fmt.Errorf("build runs request: %w", err)
		}
		runsReq.Header.Set("Authorization", authHeader(cfg.Token))

		runsResp, err := httpClient.Do(runsReq)
		if err != nil {
			return fmt.Errorf("fetch runs: %w", err)
		}
		defer runsResp.Body.Close()

		var runs []struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(runsResp.Body).Decode(&runs); err != nil || len(runs) == 0 {
			return fmt.Errorf("no runs found")
		}

		return streamRunLogs(cfg, slug, id, runs[0].ID, cmd)
	}
	return runCmd
}

func newScheduleLogsCmd() *cobra.Command {
	var runID int64

	logsCmd := &cobra.Command{
		Use:   "logs <slug> <name>",
		Short: "Stream logs for a schedule run",
		Args:  cobra.ExactArgs(2),
	}
	logsCmd.Flags().Int64Var(&runID, "run", 0, "Run ID (default: latest)")

	logsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug, name := args[0], args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		schedID, err := lookupScheduleID(cfg, slug, name)
		if err != nil {
			return err
		}

		if runID == 0 {
			// Fetch the latest run.
			runsURL := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs?limit=1", cfg.Host, slug, schedID)
			runsReq, err := http.NewRequest("GET", runsURL, nil)
			if err != nil {
				return fmt.Errorf("build runs request: %w", err)
			}
			runsReq.Header.Set("Authorization", authHeader(cfg.Token))

			runsResp, err := httpClient.Do(runsReq)
			if err != nil {
				return fmt.Errorf("fetch runs: %w", err)
			}
			defer runsResp.Body.Close()

			var runs []struct {
				ID int64 `json:"id"`
			}
			if err := json.NewDecoder(runsResp.Body).Decode(&runs); err != nil || len(runs) == 0 {
				return fmt.Errorf("no runs found for schedule %q", name)
			}
			runID = runs[0].ID
		}

		return streamRunLogs(cfg, slug, schedID, runID, cmd)
	}
	return logsCmd
}

// streamRunLogs streams (or dumps) logs for a specific schedule run.
func streamRunLogs(cfg *cliConfig, slug string, schedID, runID int64, cmd *cobra.Command) error {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d/logs", cfg.Host, slug, schedID, runID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Accept", "text/event-stream")

	// Use http.DefaultClient — no timeout for streaming log connections.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fmt.Fprintln(cmd.OutOrStdout(), scanner.Text())
	}
	return scanner.Err()
}
