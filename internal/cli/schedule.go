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
		newScheduleUpdateCmd(),
		newScheduleRmCmd(),
		newScheduleEnableCmd(),
		newScheduleDisableCmd(),
		newScheduleRunCmd(),
		newScheduleRunsCmd(),
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
	DSTAdvisory       *string  `json:"dst_advisory"`
	FirstFireRunID    *int64   `json:"first_fire_run_id"`
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
		return nil, httpError(cfg.Token, "list schedules", resp, out)
	}

	var schedules []scheduleDTO
	if err := json.NewDecoder(resp.Body).Decode(&schedules); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return schedules, nil
}

func newScheduleLsCmd() *cobra.Command {
	f := &listFlags{}
	lsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List scheduled jobs for an app",
		Args:  cobra.ExactArgs(1),
	}
	addListFlags(lsCmd, f)
	lsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		schedules, err := listSchedules(cfg, args[0])
		if err != nil {
			return err
		}

		// Convert to []map[string]any for the shared list helper.
		items := make([]map[string]any, len(schedules))
		for i, s := range schedules {
			items[i] = map[string]any{
				"id":                 s.ID,
				"name":               s.Name,
				"cron_expr":          s.CronExpr,
				"command":            s.Command,
				"enabled":            s.Enabled,
				"timeout_seconds":    s.TimeoutSeconds,
				"overlap_policy":     s.OverlapPolicy,
				"missed_policy":      s.MissedPolicy,
				"effective_timezone": s.EffectiveTimezone,
				"timezone_inherited": s.TimezoneInherited,
			}
		}

		return renderList(cmd, f, items, nil, func(w io.Writer, rendered []map[string]any) {
			if len(rendered) == 0 {
				fmt.Fprintln(w, "No schedules.")
				return
			}
			fmt.Fprintf(w, "%-6s  %-24s  %-20s  %-8s  %-28s  %s\n",
				"ID", "NAME", "CRON", "ENABLED", "TIMEZONE", "COMMAND")
			for _, item := range rendered {
				id := fmt.Sprintf("%v", item["id"])
				name := fmt.Sprintf("%v", item["name"])
				cron := fmt.Sprintf("%v", item["cron_expr"])
				enabled := "true"
				if b, ok := item["enabled"].(bool); ok && !b {
					enabled = "false"
				}
				tzDisplay := fmt.Sprintf("%v", item["effective_timezone"])
				if inherited, ok := item["timezone_inherited"].(bool); ok && inherited {
					tzDisplay += " (inherited)"
				}
				cmdParts, _ := item["command"].([]string)
				cmdStr := strings.Join(cmdParts, " ")
				fmt.Fprintf(w, "%-6s  %-24s  %-20s  %-8s  %-28s  %s\n",
					id, name, cron, enabled, tzDisplay, cmdStr)
			}
		})
	}
	return lsCmd
}

func newScheduleAddCmd() *cobra.Command {
	var flags struct {
		name          string
		cron          string
		cmd           string
		cmdJSON       string
		timeout       int
		overlap       string
		missed        string
		disabled      bool
		ifNotExists   bool
		timezone      string
		runOnRegister bool
		follow        bool
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
	addCmd.Flags().BoolVar(&flags.runOnRegister, "run-on-register", false, "Fire this schedule once now if it has never succeeded (warms the cache on first deploy)")
	addCmd.Flags().BoolVar(&flags.follow, "follow", false, "With --run-on-register, stream the first-fire run's logs until it finishes")
	_ = addCmd.MarkFlagRequired("name")
	_ = addCmd.MarkFlagRequired("cron")

	addCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		// Validate the output format at command start. When --follow is set the
		// log stream is the primary data (streaming), so NDJSON is accepted and
		// -o json is rejected. When not following, this is a document command.
		// We resolve early only to catch invalid -o values before any network
		// call; the resolved format is re-derived at emit time (see below).
		if _, err := resolveFormat(false, flags.follow); err != nil {
			return err
		}

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
			"run_on_register": flags.runOnRegister,
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
			return httpError(cfg.Token, "create schedule", resp, out)
		}

		// A 200 (as opposed to 201) means the schedule already existed with the
		// exact same configuration; the repeat is a no-op.
		if resp.StatusCode == http.StatusOK {
			var existing scheduleDTO
			if json.Unmarshal(out, &existing) == nil {
				return renderAction(cmd, "unchanged",
					map[string]any{"slug": slug, "name": existing.Name, "id": existing.ID},
					fmt.Sprintf("schedule %q already exists with identical config (id %d)", existing.Name, existing.ID))
			}
			return renderAction(cmd, "unchanged",
				map[string]any{"slug": slug, "name": flags.name},
				fmt.Sprintf("schedule %q already exists with identical config", flags.name))
		}

		var created scheduleDTO
		if err := json.Unmarshal(out, &created); err == nil {
			if created.DSTAdvisory != nil && *created.DSTAdvisory != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+*created.DSTAdvisory)
			}
			fields := map[string]any{"slug": slug, "name": created.Name, "id": created.ID}
			prose := fmt.Sprintf("created schedule %q (id %d)", created.Name, created.ID)
			if created.FirstFireRunID != nil {
				fields["first_fire_run_id"] = *created.FirstFireRunID
				prose += fmt.Sprintf("\nfirst-fire triggered (run #%d)", *created.FirstFireRunID)
			}
			if flags.follow && created.FirstFireRunID != nil {
				// In follow mode the run's log stream is the primary data on
				// stdout; its format was resolved as streaming at command start.
				// Route the creation acknowledgment to stderr so it does not
				// interleave with the NDJSON log objects.
				fmt.Fprintf(cmd.ErrOrStderr(), "created schedule %q (id %d), following run #%d\n",
					created.Name, created.ID, *created.FirstFireRunID)
				if err := streamRunLogs(cfg, slug, created.ID, *created.FirstFireRunID, true, cmd); err != nil {
					return err
				}
				return runFinalExitError(cfg, slug, created.ID, *created.FirstFireRunID)
			}
			// Non-follow path: the creation envelope is a single document.
			// Re-resolve as non-streaming so -o json is honoured and NDJSON
			// resolvedFormat set for the follow path is replaced with the
			// correct document format.
			if err := renderAction(cmd, "created", fields, prose); err != nil {
				return err
			}
		} else {
			if err := renderAction(cmd, "created",
				map[string]any{"slug": slug, "name": flags.name},
				fmt.Sprintf("%s: schedule %q created", slug, flags.name)); err != nil {
				return err
			}
		}
		return nil
	}
	return addCmd
}

func newScheduleUpdateCmd() *cobra.Command {
	var flags struct {
		cron     string
		cmd      string
		cmdJSON  string
		timeout  int
		overlap  string
		missed   string
		enabled  bool
		timezone string
		clearTZ  bool
	}

	updateCmd := &cobra.Command{
		Use:   "update <slug> <name>",
		Short: "Update an existing scheduled job in place (preserves run history)",
		Long: `Update a scheduled job without deleting and recreating it.

Only the flags you supply are changed; every other field keeps its stored
value. This preserves the schedule's run history, which a delete+recreate would
discard via ON DELETE CASCADE.

Timezone is tri-state:
  (flag omitted)        leave the per-schedule timezone unchanged
  --timezone <zone>     set an explicit IANA zone (e.g. Europe/Amsterdam)
  --clear-timezone      clear it so the schedule inherits the server default`,
		Args: cobra.ExactArgs(2),
	}
	updateCmd.Flags().StringVar(&flags.cron, "cron", "", "Cron expression, e.g. '0 * * * *'")
	updateCmd.Flags().StringVar(&flags.cmd, "cmd", "", "Command as a shell string, e.g. 'python run.py --flag x'")
	updateCmd.Flags().StringVar(&flags.cmdJSON, "cmd-json", "", `Command as a JSON array, e.g. '["python","run.py"]'`)
	updateCmd.Flags().IntVar(&flags.timeout, "timeout", 0, "Timeout in seconds (1..86400)")
	updateCmd.Flags().StringVar(&flags.overlap, "overlap", "", "Overlap policy: skip|queue|concurrent")
	updateCmd.Flags().StringVar(&flags.missed, "missed", "", "Missed-run policy: skip|run_once")
	updateCmd.Flags().BoolVar(&flags.enabled, "enabled", true, "Enabled state (use --enabled=false to disable)")
	updateCmd.Flags().StringVar(&flags.timezone, "timezone", "", "Set the per-schedule IANA timezone")
	updateCmd.Flags().BoolVar(&flags.clearTZ, "clear-timezone", false, "Clear the per-schedule timezone (inherit server default)")

	updateCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug, name := args[0], args[1]

		changed := cmd.Flags().Changed
		if changed("cmd") && changed("cmd-json") {
			return fmt.Errorf("specify at most one of --cmd or --cmd-json")
		}
		if changed("timezone") && flags.clearTZ {
			return fmt.Errorf("specify at most one of --timezone or --clear-timezone")
		}

		payload := map[string]any{}
		if changed("cron") {
			payload["cron_expr"] = flags.cron
		}
		switch {
		case changed("cmd"):
			command, err := shellwords.Parse(flags.cmd)
			if err != nil {
				return fmt.Errorf("parse --cmd: %w", err)
			}
			payload["command"] = command
		case changed("cmd-json"):
			var command []string
			if err := json.Unmarshal([]byte(flags.cmdJSON), &command); err != nil {
				return fmt.Errorf("parse --cmd-json: %w", err)
			}
			payload["command"] = command
		}
		if changed("timeout") {
			payload["timeout_seconds"] = flags.timeout
		}
		if changed("overlap") {
			payload["overlap_policy"] = flags.overlap
		}
		if changed("missed") {
			payload["missed_policy"] = flags.missed
		}
		if changed("enabled") {
			payload["enabled"] = flags.enabled
		}
		switch {
		case flags.clearTZ:
			payload["timezone"] = nil
		case changed("timezone"):
			payload["timezone"] = flags.timezone
		}

		if len(payload) == 0 {
			return fmt.Errorf("nothing to update: supply at least one field flag (see --help)")
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		id, err := lookupScheduleID(cfg, slug, name)
		if err != nil {
			return err
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

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
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "update schedule", resp, out)
		}

		return renderAction(cmd, "updated",
			map[string]any{"slug": slug, "name": name},
			fmt.Sprintf("%s: updated schedule %q", slug, name))
	}
	return updateCmd
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
				return httpError(cfg.Token, "remove schedule", resp, out)
			}

			return renderAction(cmd, "removed",
				map[string]any{"slug": slug, "name": name},
				fmt.Sprintf("%s: removed schedule %q", slug, name))
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
			return httpError(cfg.Token, "update schedule state", resp, out)
		}

		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		return renderAction(cmd, state,
			map[string]any{"slug": slug, "name": name},
			fmt.Sprintf("%s: schedule %q %s", slug, name, state))
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

		// Resolve format once at command start: streaming=true when following
		// (the log stream is the data), non-streaming when not following
		// (a single action envelope is emitted). This ensures -o ndjson is
		// rejected on the non-follow path and accepted only on the follow path.
		if _, err := resolveFormat(false, follow); err != nil {
			return err
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		sched, err := lookupSchedule(cfg, slug, name)
		if err != nil {
			return err
		}
		id := sched.ID

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
			return httpError(cfg.Token, "run schedule", resp, out)
		}

		// Only report the disabled-schedule override after the server accepted
		// the run, so a failed trigger never reads as if it proceeded.
		if !sched.Enabled {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: schedule %q is disabled; manual trigger proceeded anyway\n", name)
		}

		if !follow {
			return renderAction(cmd, "started",
				map[string]any{"slug": slug, "name": name},
				fmt.Sprintf("%s: schedule %q started", slug, name))
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

		if _, err := resolveFormat(false, true); err != nil {
			return err
		}

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
		return httpError(cfg.Token, "stream schedule logs", resp, out)
	}

	// Schedule run logs are not replica-scoped, so omit the "replica" key.
	sw := newStreamWriter(cmd.OutOrStdout(), currentFormat(), nil)
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
		sw.WriteLine(line)
	}
	return scanner.Err()
}

// scheduleRunResult is the subset of a schedule run's JSON used to derive an
// honest CLI exit code after following a run to completion.
type scheduleRunResult struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
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
		return httpError(cfg.Token, "fetch run status", resp, out)
	}

	var run scheduleRunResult
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return fmt.Errorf("decode run status: %w", err)
	}

	switch run.Status {
	case "succeeded", "skipped_overlap":
		return nil
	}
	// Mirror the command's own exit code when the run recorded one; an
	// interrupted run (null exit_code) or a recorded 0 on a non-success status
	// falls back to 1 so scripted callers still see a failure.
	code := 1
	if run.ExitCode != nil && *run.ExitCode != 0 {
		code = *run.ExitCode
	}
	return &ExitCodeError{Code: code, Kind: KindJobFailed, Err: fmt.Errorf("run %s", run.Status)}
}
