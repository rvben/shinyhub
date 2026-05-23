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
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	CronExpr          string   `json:"cron_expr"`
	Command           []string `json:"command"`
	Enabled           bool     `json:"enabled"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	OverlapPolicy     string   `json:"overlap_policy"`
	MissedPolicy      string   `json:"missed_policy"`
	Timezone          *string  `json:"timezone"`
	EffectiveTimezone string   `json:"effective_timezone"`
	TimezoneInherited bool     `json:"timezone_inherited"`
}

// lookupScheduleID resolves a schedule name to its numeric ID by listing all
// schedules for the app and returning the ID of the first match.
func lookupScheduleID(cfg *cliConfig, slug, name string) (int64, error) {
	s, err := lookupSchedule(cfg, slug, name)
	if err != nil {
		return 0, err
	}
	return s.ID, nil
}

// lookupSchedule resolves a schedule by name to its full DTO so callers can read
// fields beyond the ID (e.g. Enabled, to note a manual trigger of a disabled job).
func lookupSchedule(cfg *cliConfig, slug, name string) (scheduleDTO, error) {
	schedules, err := listSchedules(cfg, slug)
	if err != nil {
		return scheduleDTO{}, err
	}
	for _, s := range schedules {
		if s.Name == name {
			return s, nil
		}
	}
	return scheduleDTO{}, fmt.Errorf("schedule %q not found for app %q", name, slug)
}

// listSchedulesRaw returns the raw JSON response body for an app's schedules.
func listSchedulesRaw(cfg *cliConfig, slug string) ([]byte, error) {
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

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, body)
	}
	return body, nil
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
	var flags struct {
		jsonOutput bool
	}
	lsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List scheduled jobs for an app",
		Args:  cobra.ExactArgs(1),
	}
	lsCmd.Flags().BoolVar(&flags.jsonOutput, "json", false, "Output as JSON")
	lsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		if flags.jsonOutput {
			raw, err := listSchedulesRaw(cfg, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return nil
		}

		schedules, err := listSchedules(cfg, args[0])
		if err != nil {
			return err
		}
		if len(schedules) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No schedules.")
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-6s  %-24s  %-20s  %-8s  %-28s  %s\n",
			"ID", "NAME", "CRON", "ENABLED", "TIMEZONE", "COMMAND")
		for _, s := range schedules {
			cmdStr := strings.Join(s.Command, " ")
			enabled := "true"
			if !s.Enabled {
				enabled = "false"
			}
			tzDisplay := s.EffectiveTimezone
			if s.TimezoneInherited {
				tzDisplay += " (inherited)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-6d  %-24s  %-20s  %-8s  %-28s  %s\n",
				s.ID, s.Name, s.CronExpr, enabled, tzDisplay, cmdStr)
		}
		return nil
	}
	return lsCmd
}

func newScheduleAddCmd() *cobra.Command {
	var flags struct {
		name        string
		cron        string
		cmd         string
		cmdJSON     string
		timeout     int
		overlap     string
		missed      string
		disabled    bool
		ifNotExists bool
		timezone    string
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
	addCmd.Flags().BoolVar(&flags.ifNotExists, "if-not-exists", false, "Exit silently if a same-named schedule already exists")
	addCmd.Flags().StringVar(&flags.timezone, "timezone", "", "IANA timezone for this schedule (e.g. Europe/Amsterdam); empty inherits server default")
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
			"timezone":        flags.timezone,
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
		if resp.StatusCode == 409 && flags.ifNotExists {
			return nil
		}
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

		sched, err := lookupSchedule(cfg, slug, name)
		if err != nil {
			return err
		}
		id := sched.ID
		if !sched.Enabled {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: schedule %q is disabled; manual trigger proceeded anyway\n", name)
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

		// Follow the exact run that was just admitted, using the run_id from
		// the trigger response. Re-querying the latest run would race a
		// concurrent cron tick and could attach to (and report the exit code
		// of) a different run.
		var started struct {
			RunID int64 `json:"run_id"`
		}
		if err := json.Unmarshal(out, &started); err != nil {
			return fmt.Errorf("decode run response: %w", err)
		}
		if started.RunID == 0 {
			return fmt.Errorf("server did not return a run_id to follow")
		}

		if err := streamRunLogs(cfg, slug, id, started.RunID, true, cmd); err != nil {
			return err
		}
		return runFinalExitError(cfg, slug, id, started.RunID)
	}
	return runCmd
}

func newScheduleLogsCmd() *cobra.Command {
	var runID int64
	var follow bool

	logsCmd := &cobra.Command{
		Use:   "logs <slug> <name>",
		Short: "Stream logs for a schedule run",
		Args:  cobra.ExactArgs(2),
	}
	logsCmd.Flags().Int64Var(&runID, "run", 0, "Run ID (default: latest)")
	logsCmd.Flags().BoolVar(&follow, "follow", false, "Follow until the run finishes")

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
			if err := json.NewDecoder(runsResp.Body).Decode(&runs); err != nil {
				return fmt.Errorf("decode runs: %w", err)
			}
			if len(runs) == 0 {
				return fmt.Errorf("no runs found for schedule %q", name)
			}
			runID = runs[0].ID
		}

		if err := streamRunLogs(cfg, slug, schedID, runID, follow, cmd); err != nil {
			return err
		}
		// When following to completion, exit with the run's final status so
		// `schedule logs --follow` is usable as a wait-and-check in scripts.
		if follow {
			return runFinalExitError(cfg, slug, schedID, runID)
		}
		return nil
	}
	return logsCmd
}

// streamRunLogs streams (or dumps) logs for a specific schedule run.
// follow=true keeps the connection open until the run finishes; follow=false
// asks the server to send the buffered log and close.
//
// The server returns plain text for follow=false and an SSE event stream for
// follow=true. When the response is an event stream the CLI unwraps the
// "data: " framing and drops heartbeat comments and blank separator lines so
// the user only ever sees raw log content.
func streamRunLogs(cfg *cliConfig, slug string, schedID, runID int64, follow bool, cmd *cobra.Command) error {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d/logs?follow=%t", cfg.Host, slug, schedID, runID, follow)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if follow {
		req.Header.Set("Accept", "text/event-stream")
	}

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

	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if isSSE {
			if line == "" || strings.HasPrefix(line, ":") {
				continue // blank separator or heartbeat comment
			}
			if !strings.HasPrefix(line, "data:") {
				continue // ignore non-data SSE fields (event:, id:, retry:)
			}
			line = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	return scanner.Err()
}

// scheduleRunResult is the subset of a schedule run's JSON used to derive an
// honest CLI exit code after following a run to completion.
type scheduleRunResult struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

// runFinalExitError fetches the run's final state and returns an ExitCodeError
// when the run did not succeed. A run is successful when it succeeded outright
// or was skipped by the overlap policy; any other terminal state (failed,
// timed_out, cancelled) is surfaced as a non-zero exit so scripted callers and
// CI can detect failures. The exit code mirrors the scheduled command's own
// exit code when available, falling back to 1.
func runFinalExitError(cfg *cliConfig, slug string, schedID, runID int64) error {
	url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs/%d", cfg.Host, slug, schedID, runID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("build run-status request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch run status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}

	var run scheduleRunResult
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return fmt.Errorf("decode run status: %w", err)
	}

	switch run.Status {
	case "succeeded", "skipped_overlap":
		return nil
	}
	code := run.ExitCode
	if code == 0 {
		code = 1
	}
	return &ExitCodeError{Code: code, Err: fmt.Errorf("run %s", run.Status)}
}
