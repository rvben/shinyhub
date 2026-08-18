package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/appmetaspec"
	"github.com/rvben/shinyhub/internal/deploy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
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
		newAppsOpenCmd(),
		newAppsMetricsCmd(),
		newAppsLogsCmd(),
		newAppsRollbackCmd(),
		newAppsRestartCmd(),
		newAppsStartCmd(),
		newAppsSetCmd(),
		newAppsAccessCmd(),
		newAppsTransferCmd(),
		newAppsDeleteCmd(),
		newAppsStopCmd(),
		newAppsSleepCmd(),
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

func newAppsListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsList(cmd, f)
		},
	}
	addListFlags(cmd, f)
	return cmd
}

func runAppsList(cmd *cobra.Command, f *listFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	apps, total, err := getPaginatedList(cfg, "list apps", "/api/apps", f)
	if err != nil {
		return err
	}
	return renderServerList(cmd, f, apps, total, nil, func(w io.Writer, items []map[string]any) {
		if len(items) == 0 {
			fmt.Fprintln(w, "No apps.")
			return
		}
		writeAppsTable(w, items)
	})
}

// writeAppsTable renders the apps list, coloring each status by what it means
// so a stopped or failed app is findable in a long list without reading every
// row. The status word is printed either way; the color only weights it.
func writeAppsTable(w io.Writer, items []map[string]any) {
	t := newTable("SLUG", "STATUS", "DEPLOYS").alignRight(2)
	for _, a := range items {
		t.row(txt(a["slug"]), statusTxt(a["status"]), txt(a["deploy_count"]))
	}
	t.render(w)
}

// ── apps show ───────────────────────────────────────────────────────────────

type appsShowFlags struct {
	jsonOutput bool
}

func newAppsShowCmd() *cobra.Command {
	f := &appsShowFlags{}
	cmd := &cobra.Command{
		Use:     "show <slug>",
		Aliases: []string{"get"},
		Short:   "Show detailed information about an app",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsShow(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsShow(cmd *cobra.Command, args []string, f *appsShowFlags) error {
	format, err := resolveFormat(f.jsonOutput, false)
	if err != nil {
		return err
	}
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
		return httpError(cfg.Token, "show app", resp, out)
	}

	if format == formatJSON {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var resp2 struct {
		App struct {
			Slug                             string   `json:"slug"`
			Name                             string   `json:"name"`
			OwnerID                          int64    `json:"owner_id"`
			Access                           string   `json:"access"`
			Status                           string   `json:"status"`
			DesiredStatus                    string   `json:"desired_status"`
			Replicas                         int      `json:"replicas"`
			ReplicasRunning                  *int     `json:"replicas_running"`
			WorkersRunning                   int      `json:"workers_running"`
			MaxSessionsPerReplica            int      `json:"max_sessions_per_replica"`
			DeployCount                      int      `json:"deploy_count"`
			HibernateTimeoutMinutes          *int     `json:"hibernate_timeout_minutes"`
			EffectiveHibernateTimeoutMinutes *float64 `json:"effective_hibernate_timeout_minutes"`
			LastReplicaError                 string   `json:"last_replica_error"`
			MemoryLimitMB                    *int     `json:"memory_limit_mb"`
			CPUQuotaPercent                  *int     `json:"cpu_quota_percent"`
			ProjectSlug                      string   `json:"project_slug,omitempty"`
			CreatedAt                        string   `json:"created_at"`
			UpdatedAt                        string   `json:"updated_at"`
			AutoscaleEnabled                 bool     `json:"autoscale_enabled"`
			AutoscaleMinReplicas             int      `json:"autoscale_min_replicas"`
			AutoscaleMaxReplicas             int      `json:"autoscale_max_replicas"`
			AutoscaleTarget                  float64  `json:"autoscale_target"`
			RenderSeconds                    float64  `json:"render_seconds"`
			WorkerIsolation                  string   `json:"worker_isolation"`
			EffectiveWorkerIsolation         string   `json:"effective_worker_isolation"`
			WorkerGroupedSize                int      `json:"worker_grouped_size"`
			WorkerMaxWorkers                 int      `json:"worker_max_workers"`
			WorkerWarmSpares                 int      `json:"worker_warm_spares"`
			WorkerMaxSessionLifetimeSecs     int      `json:"worker_max_session_lifetime_secs"`
		} `json:"app"`
		RenderPacing *struct {
			EffectiveCores                        float64 `json:"effective_cores"`
			CoresSource                           string  `json:"cores_source"`
			SuggestedMaxSessionsPerReplica        int     `json:"suggested_max_sessions_per_replica"`
			CurrentEffectiveMaxSessionsPerReplica int     `json:"current_effective_max_sessions_per_replica"`
			CadenceAssumptionSeconds              float64 `json:"cadence_assumption_seconds"`
		} `json:"render_pacing"`
		EffectiveMaxSessionsPerReplica *int     `json:"effective_max_sessions_per_replica"`
		EffectiveAutoscaleTarget       *float64 `json:"effective_autoscale_target"`
		ReplicasStatus                 []struct {
			Index        int    `json:"index"`
			Status       string `json:"status"`
			PID          *int   `json:"pid"`
			Port         *int   `json:"port"`
			Reason       string `json:"reason"`
			ExitCode     *int   `json:"exit_code"`
			Signal       string `json:"signal"`
			RestartCount int    `json:"restart_count"`
		} `json:"replicas_status"`
		RejectsByReason *struct {
			WindowSeconds int               `json:"window_seconds"`
			Counts        map[string]uint64 `json:"counts"`
		} `json:"rejects_by_reason"`
		WorkerPool *struct {
			Mode              string `json:"mode"`
			SessionsPerWorker int    `json:"sessions_per_worker"`
			MaxWorkers        int    `json:"max_workers"`
			WarmSpareTarget   int    `json:"warm_spare_target"`
			Ceiling           int    `json:"ceiling"`
			Workers           []struct {
				SlotID    int    `json:"slot_id"`
				Status    string `json:"status"`
				Sessions  int    `json:"sessions"`
				PID       int    `json:"pid"`
				Port      int    `json:"port"`
				WarmSpare bool   `json:"warm_spare"`
			} `json:"workers"`
		} `json:"worker_pool"`
	}
	if err := json.Unmarshal(out, &resp2); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	w := cmd.OutOrStdout()
	a := resp2.App
	actualReplicas := 0
	if a.ReplicasRunning != nil {
		actualReplicas = *a.ReplicasRunning
	} else {
		for _, rep := range resp2.ReplicasStatus {
			if rep.Status == "running" {
				actualReplicas++
			}
		}
	}
	fmt.Fprintf(w, "Slug:        %s\n", a.Slug)
	fmt.Fprintf(w, "Name:        %s\n", a.Name)
	fmt.Fprintf(w, "Status:      %s\n", stylerFor(w).status(a.Status))
	if a.DesiredStatus != "" && a.DesiredStatus != a.Status {
		fmt.Fprintf(w, "Desired:     %s\n", stylerFor(w).status(a.DesiredStatus))
	}
	fmt.Fprintf(w, "Access:      %s\n", a.Access)
	fmt.Fprintf(w, "Owner:       user #%d\n", a.OwnerID)
	if a.ProjectSlug != "" {
		fmt.Fprintf(w, "Project:     %s\n", a.ProjectSlug)
	}
	effectiveIsolation := a.EffectiveWorkerIsolation
	if effectiveIsolation == "" {
		effectiveIsolation = a.WorkerIsolation
	}
	if effectiveIsolation == "" {
		effectiveIsolation = "multiplex"
	}
	if resp2.WorkerPool == nil {
		fmt.Fprintf(w, "Isolation:   %s", effectiveIsolation)
		if a.WorkerIsolation == "" {
			fmt.Fprint(w, " (inherited)")
		}
		fmt.Fprintln(w)
		if effectiveIsolation == "grouped" || effectiveIsolation == "per_session" {
			fmt.Fprintf(w, "Worker cap:  %d", a.WorkerMaxWorkers)
			if effectiveIsolation == "grouped" {
				fmt.Fprintf(w, " × %d sessions", a.WorkerGroupedSize)
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Warm spares: %d\n", a.WorkerWarmSpares)
		}
	}
	fmt.Fprintf(w, "Deploys:     %d\n", a.DeployCount)
	if wp := resp2.WorkerPool; wp != nil {
		// Elastic (grouped/per_session) app: the replicas/max-sess-per-replica
		// arithmetic below does not apply (those knobs are inert for elastic
		// pools), so render the pool's own capacity model instead.
		sessWord := "sessions"
		if wp.SessionsPerWorker == 1 {
			sessWord = "session"
		}
		fmt.Fprintf(w, "Isolation:   %s (%d %s/worker, max %d workers)\n",
			wp.Mode, wp.SessionsPerWorker, sessWord, wp.MaxWorkers)
		fmt.Fprintf(w, "Admission ceiling: %d × %d = %d concurrent sessions\n",
			wp.MaxWorkers, wp.SessionsPerWorker, wp.Ceiling)
		fmt.Fprintf(w, "Warm spares: %d (frozen when snapshotting is available)\n", wp.WarmSpareTarget)
	} else {
		fmt.Fprintf(w, "Replicas:    %d desired · %d running\n", a.Replicas, actualReplicas)
		// effective cap resolves the per-app value against the runtime default (0 =
		// inherit). Annotate a 0 with the resolved default and print the admission
		// ceiling (replicas × effective cap) so the bare "0" is not cryptic. An
		// older server may omit effective_max_sessions_per_replica entirely; treat
		// the absent field distinctly from a reported 0 so we never invent an
		// "unlimited" ceiling for an app that has an explicit cap.
		effKnown := resp2.EffectiveMaxSessionsPerReplica != nil
		if a.MaxSessionsPerReplica == 0 {
			if effKnown {
				fmt.Fprintf(w, "Max sess/r:  0 (runtime default: %d)\n", *resp2.EffectiveMaxSessionsPerReplica)
			} else {
				fmt.Fprintf(w, "Max sess/r:  0 (runtime default)\n")
			}
		} else {
			fmt.Fprintf(w, "Max sess/r:  %d\n", a.MaxSessionsPerReplica)
		}
		// Prefer the server-resolved effective cap; when it is absent fall back to
		// the explicit app cap. If neither is known (absent effective + inherited
		// cap), the ceiling is unresolvable client-side, so omit it rather than
		// claim unlimited.
		switch {
		case effKnown:
			if eff := *resp2.EffectiveMaxSessionsPerReplica; eff == 0 {
				fmt.Fprintf(w, "Admission ceiling: unlimited (no session cap)\n")
			} else {
				fmt.Fprintf(w, "Admission ceiling: %d × %d = %d concurrent new sessions\n", a.Replicas, eff, a.Replicas*eff)
			}
		case a.MaxSessionsPerReplica > 0:
			eff := a.MaxSessionsPerReplica
			fmt.Fprintf(w, "Admission ceiling: %d × %d = %d concurrent new sessions\n", a.Replicas, eff, a.Replicas*eff)
		}
	}
	if a.EffectiveHibernateTimeoutMinutes == nil {
		if a.HibernateTimeoutMinutes == nil {
			fmt.Fprintln(w, "Hibernate:   (global default)")
		} else if *a.HibernateTimeoutMinutes == 0 {
			fmt.Fprintln(w, "Hibernate:   disabled")
		} else {
			fmt.Fprintf(w, "Hibernate:   %d minutes\n", *a.HibernateTimeoutMinutes)
		}
	} else if *a.EffectiveHibernateTimeoutMinutes == 0 {
		fmt.Fprintln(w, "Hibernate:   disabled")
	} else if a.HibernateTimeoutMinutes == nil {
		fmt.Fprintf(w, "Hibernate:   %g minutes (inherited)\n", *a.EffectiveHibernateTimeoutMinutes)
	} else {
		fmt.Fprintf(w, "Hibernate:   %g minutes\n", *a.EffectiveHibernateTimeoutMinutes)
	}
	if a.LastReplicaError != "" {
		fmt.Fprintf(w, "Last replica error: %s\n", a.LastReplicaError)
	}
	// Autoscale summary: when enabled, resolve the effective target (the app's
	// own value, or the runtime default the server reports when the app's is 0)
	// so a 0 never reads as a literal "0%".
	if a.AutoscaleEnabled {
		target := a.AutoscaleTarget
		if resp2.EffectiveAutoscaleTarget != nil {
			target = *resp2.EffectiveAutoscaleTarget
		}
		fmt.Fprintf(w, "Autoscale:   on (replicas %d-%d, target %.0f%%)\n",
			a.AutoscaleMinReplicas, a.AutoscaleMaxReplicas, target*100)
	} else {
		fmt.Fprintln(w, "Autoscale:   off")
	}
	// Render pacing is the admission gate's only dial, and its effect is invisible
	// until a burst arrives, so it is reported on every app (like autoscale)
	// rather than only when on. When the server sends its advisory block, the cap
	// suggestion is printed too, but only when the app's current cap actually
	// exceeds what this host sustains: an advisory that fires on a well-tuned app
	// is one an operator learns to ignore. A current cap of 0 is unlimited, which
	// exceeds any finite suggestion.
	if a.RenderSeconds > 0 {
		fmt.Fprintf(w, "Pacing:      %gs/render\n", a.RenderSeconds)
		if rp := resp2.RenderPacing; rp != nil {
			if rp.EffectiveCores > 0 {
				src := ""
				if rp.CoresSource != "" {
					src = " via " + rp.CoresSource
				}
				fmt.Fprintf(w, "  paced for ~%g cores%s\n", rp.EffectiveCores, src)
			}
			cur := rp.CurrentEffectiveMaxSessionsPerReplica
			if rp.SuggestedMaxSessionsPerReplica > 0 && (cur == 0 || rp.SuggestedMaxSessionsPerReplica < cur) {
				current := fmt.Sprintf("%d", cur)
				if cur == 0 {
					current = "unlimited"
				}
				fmt.Fprintf(w, "  suggested max sess/r: %d (currently %s, assuming one render per session every %gs)\n",
					rp.SuggestedMaxSessionsPerReplica, current, rp.CadenceAssumptionSeconds)
			}
		}
	} else {
		fmt.Fprintln(w, "Pacing:      off")
	}
	if rr := resp2.RejectsByReason; rr != nil && len(rr.Counts) > 0 {
		mins := rr.WindowSeconds / 60
		fmt.Fprintf(w, "rejects (last %dm):\n", mins)
		// Stable ordering so the output is deterministic.
		reasons := make([]string, 0, len(rr.Counts))
		for reason := range rr.Counts {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(w, "  %s: %d\n", reason, rr.Counts[reason])
		}
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
	// For elastic apps the Workers table below is the live truth; a lingering
	// replicas_status row (from a deploy before the isolation switch) would
	// render a stale duplicate next to it, so the multiplex section is
	// suppressed whenever a worker pool exists.
	if len(resp2.ReplicasStatus) > 0 && resp2.WorkerPool == nil {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Replicas:")
		t := newTable("INDEX", "STATUS", "PID", "PORT").alignRight(0, 2, 3).indent(2)
		for _, r := range resp2.ReplicasStatus {
			pid, port := "-", "-"
			if r.PID != nil {
				pid = fmt.Sprintf("%d", *r.PID)
			}
			if r.Port != nil {
				port = fmt.Sprintf("%d", *r.Port)
			}
			diagnostic := r.Reason
			if r.Signal != "" {
				diagnostic = strings.TrimSpace(diagnostic + " · " + r.Signal)
			} else if r.ExitCode != nil {
				diagnostic = strings.TrimSpace(diagnostic + fmt.Sprintf(" · exit %d", *r.ExitCode))
			}
			if r.RestartCount > 0 {
				diagnostic = strings.TrimSpace(diagnostic + fmt.Sprintf(" · restart %d", r.RestartCount))
			}
			t.row(txt(r.Index), statusTxt(r.Status), dimTxt(pid), dimTxt(port)).note(strings.TrimPrefix(diagnostic, "· "))
		}
		t.render(w)
	}
	if wp := resp2.WorkerPool; wp != nil {
		fmt.Fprintln(w, "")
		if len(wp.Workers) == 0 {
			if wp.WarmSpareTarget > 0 {
				fmt.Fprintln(w, "Workers:     none yet (warm spares provisioning)")
			} else {
				fmt.Fprintln(w, "Workers:     none yet (spawned on demand)")
			}
		} else {
			bound := 0
			for _, wk := range wp.Workers {
				bound += wk.Sessions
			}
			fmt.Fprintf(w, "Workers:     %d live · %d/%d sessions\n", len(wp.Workers), bound, wp.Ceiling)
			t := newTable("SLOT", "STATUS", "WARM", "SESSIONS", "PID", "PORT").
				alignRight(0, 3, 4, 5).indent(2)
			for _, wk := range wp.Workers {
				pid, port := "-", "-"
				warm := "-"
				if wk.WarmSpare {
					warm = "yes"
				}
				if wk.PID != 0 {
					pid = fmt.Sprintf("%d", wk.PID)
				}
				if wk.Port != 0 {
					port = fmt.Sprintf("%d", wk.Port)
				}
				t.row(txt(wk.SlotID), statusTxt(wk.Status), txt(warm),
					txt(fmt.Sprintf("%d/%d", wk.Sessions, wp.SessionsPerWorker)),
					dimTxt(pid), dimTxt(port))
			}
			t.render(w)
		}
	}
	return nil
}

// ── apps logs ───────────────────────────────────────────────────────────────

// isStdoutTTY is an indirection seam so tests can stub TTY detection for
// stdout without faking a real terminal. Production code uses isTTY(os.Stdout).
var isStdoutTTY = func() bool { return isTTY(os.Stdout) }

type appsLogsFlags struct {
	tail     int
	follow   bool
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
			"Default behavior is TTY-aware: when stdout is an interactive terminal the\n" +
			"command opens a Server-Sent Events stream that emits the last --tail lines\n" +
			"then follows new output until interrupted. When stdout is not a terminal\n" +
			"(piped, redirected, or CI) the command performs a one-shot fetch and exits,\n" +
			"making it safe to use in scripts and grep pipelines without --no-follow.\n" +
			"\n" +
			"Explicit override flags:\n" +
			"  -f / --follow    Always stream (even when piped)\n" +
			"  --no-follow      Always one-shot (even when on a terminal)\n" +
			"\n" +
			"Passing both --follow and --no-follow at the same time is an error.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsLogs(cmd, args, f)
		},
	}
	cmd.Flags().IntVar(&f.tail, "tail", 200,
		"Number of initial lines to emit (1..10000)")
	cmd.Flags().BoolVarP(&f.follow, "follow", "f", false,
		"Force streaming even when stdout is not a terminal")
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
	if f.follow && f.noFollow {
		return validationErr("--follow and --no-follow are mutually exclusive", "pass at most one")
	}

	// Resolve whether to stream (SSE) or fetch one-shot (plain text).
	// Explicit flags take priority; otherwise fall back to TTY detection.
	stream := isStdoutTTY()
	if f.follow {
		stream = true
	}
	if f.noFollow {
		stream = false
	}

	format, err := resolveFormat(false, true)
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/apps/%s/logs?tail=%d&replica=%d",
		cfg.Host, args[0], f.tail, f.replica)
	if !stream {
		url += "&follow=false"
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))

	// One-shot fetch uses the bounded-timeout client so a stalled server
	// doesn't pin the CLI forever. The streaming path uses the untimed client
	// since SSE connections are long-lived by design.
	client := streamClient
	if !stream {
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
		return httpError(cfg.Token, "stream logs", resp, out)
	}

	// `apps logs` always streams a specific replica (default 0), so the stream
	// is replica-scoped: pass the index by pointer so even replica 0 is tagged.
	sw := newStreamWriter(cmd.OutOrStdout(), format, &f.replica)
	if !stream {
		// Plain-text response: scan lines and route through the stream writer
		// so NDJSON mode wraps each line correctly.
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			sw.WriteLine(scanner.Text())
		}
		return scanner.Err()
	}
	// SSE stream: strip framing before handing each line to the writer.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue // blank separator or heartbeat comment
		}
		if !strings.HasPrefix(line, "data:") {
			continue // ignore non-data SSE fields (event:, id:, retry:)
		}
		line = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		sw.WriteLine(line)
	}
	return scanner.Err()
}

// ── apps rollback ───────────────────────────────────────────────────────────

type rollbackFlags struct {
	deploymentID int64
	wait         bool
	waitTimeout  time.Duration
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
	cmd.Flags().BoolVar(&f.wait, "wait", false,
		"After rolling back, wait until the app is healthy again")
	cmd.Flags().DurationVar(&f.waitTimeout, "wait-timeout", 300*time.Second,
		"How long to wait for the app to become healthy when --wait is set")
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
	req.Header.Set("X-Shinyhub-Deploy-Channel", "cli")
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
		return httpError(cfg.Token, "rollback", resp, out)
	}
	fields := map[string]any{"slug": slug}
	var prose string
	if cmd.Flags().Changed("to") {
		fields["deployment_id"] = f.deploymentID
		prose = fmt.Sprintf("%s: rolled back to deployment %d", slug, f.deploymentID)
	} else {
		prose = fmt.Sprintf("%s: rolled back to previous deployment", slug)
	}
	if err := renderAction(cmd, "rolled_back", fields, prose); err != nil {
		return err
	}
	if f.wait {
		return waitForHealthyWithOutput(cfg, slug, f.waitTimeout, cmd.ErrOrStderr())
	}
	return nil
}

// ── apps restart / start ────────────────────────────────────────────────────

type restartFlags struct {
	wait        bool
	waitTimeout time.Duration
}

func newAppsRestartCmd() *cobra.Command {
	f := &restartFlags{}
	cmd := &cobra.Command{
		Use:   "restart <slug>",
		Short: "Restart a running app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsRestart(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.wait, "wait", false,
		"After restarting, wait until the app is healthy again")
	cmd.Flags().DurationVar(&f.waitTimeout, "wait-timeout", 300*time.Second,
		"How long to wait for the app to become healthy when --wait is set")
	return cmd
}

func runAppsRestart(cmd *cobra.Command, args []string, f *restartFlags) error {
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
		return httpError(cfg.Token, "restart", resp, out)
	}
	if err := renderAction(cmd, "running",
		map[string]any{"slug": slug},
		fmt.Sprintf("%s: restarted", slug)); err != nil {
		return err
	}
	if f.wait {
		return waitForHealthyWithOutput(cfg, slug, f.waitTimeout, cmd.ErrOrStderr())
	}
	return nil
}

// newAppsStartCmd starts a stopped app without cycling a running one. It sends
// ?if_not_running=true so the server skips the restart when the app is already
// running, making the operation idempotent. `apps restart` always cycles the
// pool regardless of current state.
func newAppsStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <slug>",
		Short: "Start a stopped app (no-op if already running)",
		Args:  cobra.ExactArgs(1),
		RunE:  runAppsStart,
	}
}

func runAppsStart(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	result, err := startAppIfNotRunning(cfg, slug)
	if err != nil {
		return err
	}
	// The server returns {"status":"running","note":"already running"} when the
	// app was already up. Surface the note field so the caller can distinguish
	// a fresh start from a no-op.
	if note, _ := result["note"].(string); note != "" {
		return renderAction(cmd, "running",
			map[string]any{"slug": slug, "note": note},
			fmt.Sprintf("%s: %s", slug, note))
	}
	return renderAction(cmd, "running",
		map[string]any{"slug": slug},
		fmt.Sprintf("%s: started", slug))
}

// startAppIfNotRunning is the output-free primitive shared by `apps start`
// and `deploy --open`. The idempotency query makes it safe to use as recovery
// when an older server accepts deploy's start request but still reports that it
// kept the app stopped.
func startAppIfNotRunning(cfg *cliConfig, slug string) (map[string]any, error) {
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/restart?if_not_running=true", nil)
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
		return nil, httpError(cfg.Token, "start app", resp, body)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		// Preserve compatibility with older servers that returned an empty 2xx
		// response. The HTTP status is sufficient evidence that the idempotent
		// start request was accepted; callers do not require response fields.
		return map[string]any{}, nil
	}
	return result, nil
}

// callRestartAs hits POST /api/apps/{slug}/restart and reports the action using
// the given past-tense verb. Unlike runAppsStart it does NOT send
// ?if_not_running=true, so restart always cycles the pool.
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
			return httpError(cfg.Token, "start app", resp, out)
		}
		return renderAction(cmd, "running",
			map[string]any{"slug": slug},
			fmt.Sprintf("%s: %s", slug, pastTense))
	}
}

// ── apps set ────────────────────────────────────────────────────────────────

type appsSetFlags struct {
	hibernateTimeout      int
	replicas              int
	maxSessionsPerReplica int
	minWarmReplicas       int
	memoryLimitMB         int
	cpuQuotaPercent       int
	tiers                 []string
	autoscale             bool
	autoscaleMin          int
	autoscaleMax          int
	autoscaleTarget       float64
	renderSeconds         float64
	yes                   bool
	wait                  bool
	waitTimeout           time.Duration
	isolation             string
	groupedSize           int
	maxWorkers            int
	warmSpares            int
	maxSessionLifetime    int
	ephemeralDataOk       bool
	icon                  string
	name                  string
	description           string
	project               string
}

func newAppsSetCmd() *cobra.Command {
	f := &appsSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <slug>",
		Short: "Update app settings",
		Long: "Update app settings: scaling, hibernation, and autoscale.\n\n" +
			"Visibility and membership are not set here - use `shinyhub apps access set\n" +
			"<slug> <private|shared|public>` and `shinyhub apps access grant`.",
		Args: cobra.ExactArgs(1),
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
	cmd.Flags().IntVar(&f.minWarmReplicas, "min-warm-replicas", 0,
		"Pre-warming floor: replicas kept running during idle hibernation (0..1000)")
	cmd.Flags().IntVar(&f.memoryLimitMB, "memory-limit-mb", 0,
		"Per-replica memory ceiling in MiB (-1 = clear/inherit global, 0 = unlimited, 16..1048576 = explicit). Restarts the app.")
	cmd.Flags().IntVar(&f.cpuQuotaPercent, "cpu-quota-percent", 0,
		"Per-replica CPU ceiling in percent of one core (-1 = clear/inherit, 0 = unlimited, 1..6400; 100 = 1 core, 150 = 1.5 cores). Restarts the app.")
	cmd.Flags().StringArrayVar(&f.tiers, "tier", nil,
		"Per-tier replica placement as name=count (repeatable, e.g. --tier local=2 --tier burst=1); mutually exclusive with --replicas")
	cmd.Flags().BoolVar(&f.autoscale, "autoscale", false,
		"Enable (--autoscale) or disable (--autoscale=false) session-saturation replica autoscaling for this app")
	cmd.Flags().IntVar(&f.autoscaleMin, "autoscale-min", 0,
		"Autoscale floor: minimum replicas to keep running (>= 1)")
	cmd.Flags().IntVar(&f.autoscaleMax, "autoscale-max", 0,
		"Autoscale ceiling: maximum replicas the controller may add (>= autoscale-min)")
	cmd.Flags().Float64Var(&f.autoscaleTarget, "autoscale-target", 0,
		"Target average fraction of the per-replica session cap, in (0,1] (0 = inherit the runtime default)")
	cmd.Flags().Float64Var(&f.renderSeconds, "render-seconds", 0,
		"Expected worst-case render time in seconds for this app's render callback, used to size a steady-state session cap advisory (0 = disable pacing; 0..600)")
	cmd.Flags().BoolVar(&f.yes, "yes", false,
		"Skip the confirmation prompt for a replica change (which restarts the app)")
	cmd.Flags().BoolVar(&f.wait, "wait", false,
		"After applying, wait until the app is healthy again (useful after a replica change)")
	cmd.Flags().DurationVar(&f.waitTimeout, "wait-timeout", 300*time.Second,
		"How long to wait for the app to become healthy when --wait is set")
	cmd.Flags().StringVar(&f.isolation, "isolation", "",
		"Session isolation mode: multiplex (default) | grouped | per_session")
	cmd.Flags().IntVar(&f.groupedSize, "grouped-size", 0,
		"Clients per worker when --isolation grouped (>= 1)")
	cmd.Flags().IntVar(&f.maxWorkers, "max-workers", 0,
		"Demand-driven worker ceiling for grouped/per_session (>= 1)")
	cmd.Flags().IntVar(&f.warmSpares, "warm-spares", 0,
		"Prebooted elastic workers kept ready; frozen and memory-reclaimed when snapshotting is available (0..max-workers)")
	cmd.Flags().IntVar(&f.maxSessionLifetime, "max-session-lifetime", 0,
		"Absolute worker lifetime in seconds (0 = unlimited)")
	cmd.Flags().BoolVar(&f.ephemeralDataOk, "ephemeral-data-ok", false,
		"Accept ephemeral (task-local) app-data on a Fargate tier with no durable backend: allows deploying and pushing data even though it is lost on restart/hibernation and not shared across replicas")
	cmd.Flags().StringVar(&f.icon, "icon", "",
		"Emoji shown as the app's icon (a single emoji; \"\" clears the emoji and restores any uploaded image)")
	cmd.Flags().StringVar(&f.name, "name", "",
		"Friendly display name shown on the dashboard card and detail heading (1..128 characters; the slug is unchanged)")
	cmd.Flags().StringVar(&f.description, "description", "",
		"One-line description shown under the name (up to 280 characters; \"\" clears it)")
	cmd.Flags().StringVar(&f.project, "project", "",
		"Project slug this app is grouped under on the dashboard (\"\" removes it from any project)")
	return cmd
}

func runAppsSet(cmd *cobra.Command, args []string, f *appsSetFlags) error {
	hibernateChanged := cmd.Flags().Changed("hibernate-timeout")
	replicasChanged := cmd.Flags().Changed("replicas")
	capChanged := cmd.Flags().Changed("max-sessions-per-replica")
	minWarmReplicasChanged := cmd.Flags().Changed("min-warm-replicas")
	memoryLimitChanged := cmd.Flags().Changed("memory-limit-mb")
	cpuQuotaChanged := cmd.Flags().Changed("cpu-quota-percent")
	tierChanged := cmd.Flags().Changed("tier")
	autoscaleChanged := cmd.Flags().Changed("autoscale")
	autoscaleMinChanged := cmd.Flags().Changed("autoscale-min")
	autoscaleMaxChanged := cmd.Flags().Changed("autoscale-max")
	autoscaleTargetChanged := cmd.Flags().Changed("autoscale-target")
	anyAutoscaleChanged := autoscaleChanged || autoscaleMinChanged || autoscaleMaxChanged || autoscaleTargetChanged
	renderSecondsChanged := cmd.Flags().Changed("render-seconds")
	isolationChanged := cmd.Flags().Changed("isolation")
	groupedSizeChanged := cmd.Flags().Changed("grouped-size")
	maxWorkersChanged := cmd.Flags().Changed("max-workers")
	warmSparesChanged := cmd.Flags().Changed("warm-spares")
	maxSessionLifetimeChanged := cmd.Flags().Changed("max-session-lifetime")
	anyWorkerChanged := isolationChanged || groupedSizeChanged || maxWorkersChanged || warmSparesChanged || maxSessionLifetimeChanged
	ephemeralDataOkChanged := cmd.Flags().Changed("ephemeral-data-ok")
	iconChanged := cmd.Flags().Changed("icon")
	nameChanged := cmd.Flags().Changed("name")
	descriptionChanged := cmd.Flags().Changed("description")
	projectChanged := cmd.Flags().Changed("project")

	if !hibernateChanged && !replicasChanged && !capChanged && !minWarmReplicasChanged && !tierChanged && !anyAutoscaleChanged && !memoryLimitChanged && !cpuQuotaChanged && !anyWorkerChanged && !ephemeralDataOkChanged && !renderSecondsChanged && !iconChanged && !nameChanged && !descriptionChanged && !projectChanged {
		return fmt.Errorf("at least one flag is required (e.g. --hibernate-timeout, --replicas, --tier, --max-sessions-per-replica, --min-warm-replicas, --memory-limit-mb, --cpu-quota-percent, --autoscale, --isolation, --ephemeral-data-ok, --render-seconds, --icon, --name, --description, --project)")
	}
	// Validate against the same spec the server uses so a too-long or
	// whitespace-only value fails locally with the identical message instead of
	// costing a round trip.
	if nameChanged {
		v, err := appmetaspec.NormalizeName(f.name)
		if err != nil {
			return validationErr(err.Error(), "the display name is separate from the slug; the slug is set at deploy time and cannot be changed here")
		}
		f.name = v
	}
	if descriptionChanged {
		v, err := appmetaspec.NormalizeDescription(f.description)
		if err != nil {
			return validationErr(err.Error(), "pass --description \"\" to clear the description")
		}
		f.description = v
	}
	if projectChanged {
		f.project = strings.TrimSpace(f.project)
		if f.project != "" && !slugpkg.Valid(f.project) {
			return validationErr("--project must be "+slugpkg.HumanRule,
				"pass --project \"\" to remove the app from its project")
		}
	}
	if memoryLimitChanged && f.memoryLimitMB != -1 {
		if err := deploy.ValidateMemoryLimitMB(f.memoryLimitMB); err != nil {
			return fmt.Errorf("--memory-limit-mb: %w (or -1 to clear/inherit)", err)
		}
	}
	if cpuQuotaChanged && f.cpuQuotaPercent != -1 {
		if err := deploy.ValidateCPUQuotaPercent(f.cpuQuotaPercent); err != nil {
			return fmt.Errorf("--cpu-quota-percent: %w (or -1 to clear/inherit)", err)
		}
	}
	if replicasChanged && f.replicas < 1 {
		return fmt.Errorf("--replicas must be >= 1")
	}
	if capChanged && (f.maxSessionsPerReplica < 0 || f.maxSessionsPerReplica > 1000) {
		return fmt.Errorf("--max-sessions-per-replica must be between 0 and 1000")
	}
	// Mirrors the server-side guard (Task 6) so an out-of-range or non-finite
	// value fails fast without a round trip.
	if renderSecondsChanged && (math.IsNaN(f.renderSeconds) || math.IsInf(f.renderSeconds, 0) || f.renderSeconds < 0 || f.renderSeconds > 600) {
		return validationErr("--render-seconds must be a finite number between 0 and 600", "")
	}
	if minWarmReplicasChanged && (f.minWarmReplicas < 0 || f.minWarmReplicas > 1000) {
		return fmt.Errorf("--min-warm-replicas must be between 0 and 1000")
	}
	if warmSparesChanged && (f.warmSpares < 0 || f.warmSpares > 1000) {
		return fmt.Errorf("--warm-spares must be between 0 and 1000")
	}
	if warmSparesChanged && maxWorkersChanged && f.warmSpares > f.maxWorkers {
		return fmt.Errorf("--warm-spares must be <= --max-workers")
	}
	if hibernateChanged && f.hibernateTimeout < -1 {
		return fmt.Errorf("--hibernate-timeout must be -1 (reset to global default), 0 (disable), or a positive number of minutes")
	}
	// Validate the autoscale flags client-side for fast feedback; the server
	// remains the authority on the cross-field rules (min >= 1 when enabled,
	// max <= the runtime ceiling) since those depend on server config.
	if autoscaleTargetChanged && (f.autoscaleTarget < 0 || f.autoscaleTarget > 1) {
		return fmt.Errorf("--autoscale-target must be in [0,1] (0 inherits the runtime default)")
	}
	if autoscaleMinChanged && f.autoscaleMin < 0 {
		return fmt.Errorf("--autoscale-min must be >= 0")
	}
	if autoscaleMaxChanged && f.autoscaleMax < 0 {
		return fmt.Errorf("--autoscale-max must be >= 0")
	}

	// --tier and --replicas both set the pool size/shape, so only one may be
	// given. Parse the repeatable name=count specs into a placement map the
	// server accepts directly.
	var placement map[string]int
	if tierChanged {
		if replicasChanged {
			return fmt.Errorf("--tier and --replicas are mutually exclusive")
		}
		placement = make(map[string]int, len(f.tiers))
		for _, spec := range f.tiers {
			name, countStr, ok := strings.Cut(spec, "=")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				return fmt.Errorf("--tier must be name=count, got %q", spec)
			}
			count, err := strconv.Atoi(strings.TrimSpace(countStr))
			if err != nil || count < 0 {
				return fmt.Errorf("--tier %q count must be a non-negative integer", spec)
			}
			placement[name] = count
		}
	}

	// A replica, tier, or resource-limit change restarts the app and drops
	// active sessions (the new pool/cgroup ceiling is applied at spawn).
	// --yes bypasses the interactive prompt. On a non-TTY without --yes,
	// refuse before any config or network access so the error is the first
	// thing the caller sees, not a config-not-found or auth failure.
	restartChange := replicasChanged || tierChanged || memoryLimitChanged || cpuQuotaChanged
	if restartChange && !f.yes && !isStdinTTY() {
		return confirmationRequiredError(
			"this change restarts the app and drops active sessions",
			"--yes")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]

	// On a TTY without --yes, prompt interactively before proceeding.
	if restartChange && !f.yes {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"This change restarts %q and drops active sessions. Continue? [y/N]: ", slug)
		var confirm string
		if _, err := fmt.Fscan(cmd.InOrStdin(), &confirm); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		switch strings.ToLower(confirm) {
		case "y", "yes":
			// proceed
		default:
			return fmt.Errorf("aborted - no changes applied")
		}
	}

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
	if tierChanged {
		payload["placement"] = placement
	}
	if capChanged {
		payload["max_sessions_per_replica"] = f.maxSessionsPerReplica
	}
	if renderSecondsChanged {
		payload["render_seconds"] = f.renderSeconds
	}
	if minWarmReplicasChanged {
		payload["min_warm_replicas"] = f.minWarmReplicas
	}
	if memoryLimitChanged {
		// -1 → send null (clear/inherit); 0+ → send the value (0 = unlimited).
		var v *int
		if f.memoryLimitMB != -1 {
			m := f.memoryLimitMB
			v = &m
		}
		payload["memory_limit_mb"] = v
	}
	if cpuQuotaChanged {
		var v *int
		if f.cpuQuotaPercent != -1 {
			c := f.cpuQuotaPercent
			v = &c
		}
		payload["cpu_quota_percent"] = v
	}
	if anyAutoscaleChanged {
		// Send only the fields the caller changed; the server merges them over
		// the stored values so each field can be updated independently.
		as := map[string]any{}
		if autoscaleChanged {
			as["enabled"] = f.autoscale
		}
		if autoscaleMinChanged {
			as["min_replicas"] = f.autoscaleMin
		}
		if autoscaleMaxChanged {
			as["max_replicas"] = f.autoscaleMax
		}
		if autoscaleTargetChanged {
			as["target"] = f.autoscaleTarget
		}
		payload["autoscale"] = as
	}
	if isolationChanged {
		payload["worker_isolation"] = f.isolation
	}
	if groupedSizeChanged {
		payload["worker_grouped_size"] = f.groupedSize
	}
	if maxWorkersChanged {
		payload["worker_max_workers"] = f.maxWorkers
	}
	if warmSparesChanged {
		payload["worker_warm_spares"] = f.warmSpares
	}
	if maxSessionLifetimeChanged {
		payload["worker_max_session_lifetime_secs"] = f.maxSessionLifetime
	}
	if ephemeralDataOkChanged {
		payload["ephemeral_data_ack"] = f.ephemeralDataOk
	}
	if iconChanged {
		// Built from Changed, not from a non-empty test: "" is a meaningful
		// value here (clear the emoji), so a non-empty check makes clearing a
		// silent no-op.
		payload["icon_emoji"] = f.icon
	}
	if nameChanged {
		payload["name"] = f.name
	}
	if descriptionChanged {
		// Same reason as the icon: "" clears the description, so the key is sent
		// whenever the flag was passed rather than when it is non-empty.
		payload["description"] = f.description
	}
	if projectChanged {
		// Keyed off Changed rather than a non-empty value so `--project ""`
		// reaches the server as an explicit clear.
		payload["project_slug"] = f.project
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
		return httpError(cfg.Token, "update app", resp, out)
	}
	if warn := resp.Header.Get("X-ShinyHub-Warning"); warn != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warn)
	}

	// render_pacing is present only when render_seconds > 0; a decode failure
	// (or the field's absence) leaves patchResp.RenderPacing nil, which the
	// advisory below treats as "nothing to suggest".
	var patchResp struct {
		RenderPacing *struct {
			RenderSeconds                         float64 `json:"render_seconds"`
			EffectiveCores                        float64 `json:"effective_cores"`
			CoresSource                           string  `json:"cores_source"`
			SuggestedMaxSessionsPerReplica        int     `json:"suggested_max_sessions_per_replica"`
			CurrentEffectiveMaxSessionsPerReplica int     `json:"current_effective_max_sessions_per_replica"`
			CadenceAssumptionSeconds              float64 `json:"cadence_assumption_seconds"`
		} `json:"render_pacing"`
	}
	_ = json.Unmarshal(out, &patchResp)

	var lines []string
	if hibernateChanged {
		minutes, _ := payload["hibernate_timeout_minutes"].(*int)
		switch {
		case minutes == nil:
			lines = append(lines, fmt.Sprintf("%s: hibernate-timeout reset to global default", slug))
		case *minutes == 0:
			lines = append(lines, fmt.Sprintf("%s: hibernation disabled", slug))
		default:
			lines = append(lines, fmt.Sprintf("%s: hibernate-timeout set to %d minutes", slug, *minutes))
		}
	}
	if replicasChanged {
		lines = append(lines, fmt.Sprintf("%s: replicas set to %d", slug, f.replicas))
	}
	if tierChanged {
		parts := make([]string, 0, len(f.tiers))
		total := 0
		for _, name := range sortedTierNames(placement) {
			parts = append(parts, fmt.Sprintf("%s=%d", name, placement[name]))
			total += placement[name]
		}
		lines = append(lines, fmt.Sprintf("%s: placement set to %s (%d replicas)", slug, strings.Join(parts, ", "), total))
	}
	if capChanged {
		if f.maxSessionsPerReplica == 0 {
			lines = append(lines, fmt.Sprintf("%s: max-sessions-per-replica reset to runtime default", slug))
		} else {
			lines = append(lines, fmt.Sprintf("%s: max-sessions-per-replica set to %d", slug, f.maxSessionsPerReplica))
		}
	}
	if minWarmReplicasChanged {
		fmt.Printf("%s: min-warm-replicas set to %d\n", slug, f.minWarmReplicas)
	}
	if renderSecondsChanged {
		if f.renderSeconds == 0 {
			lines = append(lines, fmt.Sprintf("%s: render-seconds pacing disabled", slug))
		} else {
			lines = append(lines, fmt.Sprintf("%s: render-seconds set to %g", slug, f.renderSeconds))
		}
	}
	if memoryLimitChanged {
		switch f.memoryLimitMB {
		case -1:
			lines = append(lines, fmt.Sprintf("%s: memory-limit cleared (inherit global default)", slug))
		case 0:
			lines = append(lines, fmt.Sprintf("%s: memory-limit set to unlimited", slug))
		default:
			lines = append(lines, fmt.Sprintf("%s: memory-limit set to %d MiB per replica", slug, f.memoryLimitMB))
		}
	}
	if cpuQuotaChanged {
		switch f.cpuQuotaPercent {
		case -1:
			lines = append(lines, fmt.Sprintf("%s: cpu-quota cleared (inherit global default)", slug))
		case 0:
			lines = append(lines, fmt.Sprintf("%s: cpu-quota set to unlimited", slug))
		default:
			lines = append(lines, fmt.Sprintf("%s: cpu-quota set to %d%% of a core per replica", slug, f.cpuQuotaPercent))
		}
	}
	if anyAutoscaleChanged {
		if autoscaleChanged {
			if f.autoscale {
				lines = append(lines, fmt.Sprintf("%s: autoscale enabled", slug))
			} else {
				lines = append(lines, fmt.Sprintf("%s: autoscale disabled", slug))
			}
		}
		if autoscaleMinChanged {
			lines = append(lines, fmt.Sprintf("%s: autoscale-min set to %d", slug, f.autoscaleMin))
		}
		if autoscaleMaxChanged {
			lines = append(lines, fmt.Sprintf("%s: autoscale-max set to %d", slug, f.autoscaleMax))
		}
		if autoscaleTargetChanged {
			if f.autoscaleTarget == 0 {
				lines = append(lines, fmt.Sprintf("%s: autoscale-target reset to runtime default", slug))
			} else {
				lines = append(lines, fmt.Sprintf("%s: autoscale-target set to %.0f%%", slug, f.autoscaleTarget*100))
			}
		}
	}
	if nameChanged {
		lines = append(lines, fmt.Sprintf("%s: name set to %q", slug, f.name))
	}
	if descriptionChanged {
		if f.description == "" {
			lines = append(lines, fmt.Sprintf("%s: description cleared", slug))
		} else {
			lines = append(lines, fmt.Sprintf("%s: description set to %q", slug, f.description))
		}
	}
	if projectChanged {
		if f.project == "" {
			lines = append(lines, fmt.Sprintf("%s: removed from its project", slug))
		} else {
			lines = append(lines, fmt.Sprintf("%s: project set to %s", slug, f.project))
		}
	}
	if iconChanged {
		if f.icon == "" {
			lines = append(lines, fmt.Sprintf("%s: icon emoji cleared", slug))
		} else {
			lines = append(lines, fmt.Sprintf("%s: icon set to %s", slug, f.icon))
		}
	}
	if err := renderAction(cmd, "updated", map[string]any{"slug": slug}, strings.Join(lines, "\n")); err != nil {
		return err
	}

	// Cap-sizing advisory: when render pacing suggests a lower per-replica
	// session cap than what is currently in effect, surface it so the caller
	// can decide whether to tighten --max-sessions-per-replica.
	if block := patchResp.RenderPacing; block != nil && block.SuggestedMaxSessionsPerReplica < block.CurrentEffectiveMaxSessionsPerReplica {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Render pacing on (render_seconds=%g). For steady-state protection on ~%g cores, "+
				"consider --max-sessions-per-replica %d (current effective: %d).\n",
			block.RenderSeconds, block.EffectiveCores,
			block.SuggestedMaxSessionsPerReplica, block.CurrentEffectiveMaxSessionsPerReplica)
	}

	if f.wait {
		return waitForHealthyWithOutput(cfg, slug, f.waitTimeout, cmd.ErrOrStderr())
	}
	return nil
}

// sortedTierNames returns the placement map's tier names in alphabetical order
// so the confirmation output is deterministic.
func sortedTierNames(placement map[string]int) []string {
	names := make([]string, 0, len(placement))
	for name := range placement {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ── apps access ─────────────────────────────────────────────────────────────

func newAppsAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage app access control",
	}
	cmd.AddCommand(
		newAppsAccessSetCmd(),
		newAppsAccessGrantCmd(),
		newAppsAccessRevokeCmd(),
		newAppsAccessListCmd(),
		newAppsAccessGroupGrantCmd(),
		newAppsAccessGroupRevokeCmd(),
		newAppsAccessGroupListCmd(),
	)
	return cmd
}

// normalizeAccessLevel maps the accepted alias "internal" onto the canonical
// access level "shared" (every signed-in user). Other values pass through
// unchanged for the server to validate, so the API surface stays canonical.
func normalizeAccessLevel(level string) string {
	if level == "internal" {
		return "shared"
	}
	return level
}

func newAppsAccessSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <slug> <level>",
		Short: "Set access level for an app (level: public, private, or shared)",
		Long: "Set who can open an app.\n\n" +
			"  private  only the owner and the members/groups granted access\n" +
			"  shared   every signed-in user (alias: internal)\n" +
			"  public   anyone who can reach the server, no sign-in required\n\n" +
			"Platform admins and operators can always open any app. Use\n" +
			"`shinyhub apps access grant` to add members to a private app.",
		Args: cobra.ExactArgs(2),
		RunE: runAppsAccessSet,
	}
}

func runAppsAccessSet(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, accessLevel := args[0], normalizeAccessLevel(args[1])
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
		return httpError(cfg.Token, "set access", resp, out)
	}
	return renderAction(cmd, "updated",
		map[string]any{"slug": slug, "access": accessLevel},
		fmt.Sprintf("%s: access set to %s", slug, accessLevel))
}

type appsAccessGrantFlags struct {
	role string
}

func newAppsAccessGrantCmd() *cobra.Command {
	f := &appsAccessGrantFlags{}
	cmd := &cobra.Command{
		Use:   "grant <slug> <username>",
		Short: "Grant a user access to an app",
		Long: "Grant a user membership of an app.\n\n" +
			"On a `private` app, membership is what admits the user: a viewer grant\n" +
			"lets them open the app, and `--role manager` additionally lets them\n" +
			"deploy, configure, and manage members. On a `shared` or `public` app,\n" +
			"where every signed-in user (or anyone, for public) can already view,\n" +
			"a grant still controls the manager role.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGrant(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.role, "role", "viewer", "Member role: viewer or manager")
	return cmd
}

func runAppsAccessGrant(cmd *cobra.Command, args []string, f *appsAccessGrantFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, username := args[0], args[1]
	payload := map[string]string{"username": username}
	// Only send role when explicitly set, so a plain `grant` never changes an
	// existing member's role (the server preserves it when role is absent).
	if cmd.Flags().Changed("role") {
		payload["role"] = f.role
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/members", bytes.NewReader(body))
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
		return httpError(cfg.Token, "grant access", resp, out)
	}
	fields := map[string]any{"slug": slug, "username": username}
	var prose string
	if cmd.Flags().Changed("role") {
		fields["role"] = f.role
		prose = fmt.Sprintf("%s: granted %s access to %s", slug, f.role, username)
	} else {
		prose = fmt.Sprintf("%s: granted access to %s", slug, username)
	}
	// The server warns when the app's visibility already admits everyone
	// (shared/public); relay it like group-grant does.
	if warn := resp.Header.Get("X-ShinyHub-Warning"); warn != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warn)
	}
	return renderAction(cmd, "granted", fields, prose)
}

func newAppsAccessRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <slug> <username>",
		Short: "Revoke a user's access to an app",
		Args:  cobra.ExactArgs(2),
		RunE:  runAppsAccessRevoke,
	}
}

func runAppsAccessRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, username := args[0], args[1]
	body, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug+"/members", bytes.NewReader(body))
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
		return httpError(cfg.Token, "revoke access", resp, out)
	}
	return renderAction(cmd, "revoked",
		map[string]any{"slug": slug, "username": username},
		fmt.Sprintf("%s: revoked access for %s", slug, username))
}

func newAppsAccessListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "list <slug>",
		Short: "List members granted access to an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessList(cmd, args, f)
		},
	}
	addListFlags(cmd, f)
	return cmd
}

func runAppsAccessList(cmd *cobra.Command, args []string, f *listFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	members, total, err := getPaginatedList(cfg, "list members", "/api/apps/"+slug+"/members", f)
	if err != nil {
		return err
	}
	return renderServerList(cmd, f, members, total, nil, func(w io.Writer, items []map[string]any) {
		if len(items) == 0 {
			fmt.Fprintf(w, "%s: no members\n", slug)
			return
		}
		t := newTable("USER", "ROLE")
		for _, m := range items {
			t.row(txt(m["username"]), txt(m["role"]))
		}
		t.render(w)
	})
}

// ── apps access group-grant / group-revoke / group-list ─────────────────────

type appsAccessGroupGrantFlags struct {
	role string
}

func newAppsAccessGroupGrantCmd() *cobra.Command {
	f := &appsAccessGroupGrantFlags{}
	cmd := &cobra.Command{
		Use:   "group-grant <slug> <group>",
		Short: "Grant an IdP group access to an app",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGroupGrant(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.role, "role", "viewer", "Group role: viewer or manager")
	return cmd
}

func runAppsAccessGroupGrant(cmd *cobra.Command, args []string, f *appsAccessGroupGrantFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, group := args[0], args[1]
	body, err := json.Marshal(map[string]string{"group": group, "role": f.role})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/group-access", bytes.NewReader(body))
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
		return httpError(cfg.Token, "grant group access", resp, out)
	}
	if warn := resp.Header.Get("X-ShinyHub-Warning"); warn != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warn)
	}
	return renderAction(cmd, "granted",
		map[string]any{"slug": slug, "group": group, "role": f.role},
		fmt.Sprintf("%s: granted %s access to group %s", slug, f.role, group))
}

func newAppsAccessGroupRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "group-revoke <slug> <group>",
		Short: "Revoke an IdP group's access to an app",
		Args:  cobra.ExactArgs(2),
		RunE:  runAppsAccessGroupRevoke,
	}
}

func runAppsAccessGroupRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, group := args[0], args[1]
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug+"/group-access/"+group, nil)
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
		return httpError(cfg.Token, "revoke group access", resp, out)
	}
	return renderAction(cmd, "revoked",
		map[string]any{"slug": slug, "group": group},
		fmt.Sprintf("%s: revoked access for group %s", slug, group))
}

func newAppsAccessGroupListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "group-list <slug>",
		Short: "List IdP group access rules for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsAccessGroupList(cmd, args, f)
		},
	}
	addListFlags(cmd, f)
	return cmd
}

func runAppsAccessGroupList(cmd *cobra.Command, args []string, f *listFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	rules, total, err := getPaginatedList(cfg, "list group access", "/api/apps/"+slug+"/group-access", f)
	if err != nil {
		return err
	}
	return renderServerList(cmd, f, rules, total, nil, func(w io.Writer, items []map[string]any) {
		if len(items) == 0 {
			fmt.Fprintf(w, "%s: no group rules\n", slug)
			return
		}
		t := newTable("GROUP", "ROLE")
		for _, r := range items {
			t.row(txt(r["group"]), txt(r["role"]))
		}
		t.render(w)
	})
}

// ── apps transfer ───────────────────────────────────────────────────────────

func newAppsTransferCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "transfer <slug> <username>",
		Short: "Transfer app ownership to another user",
		Long: "Transfer app ownership to another user.\n\n" +
			"Only the current owner or a platform admin/operator can transfer.\n" +
			"The new owner gains full manage rights on the app; the previous\n" +
			"owner keeps only whatever membership they hold. Use this before\n" +
			"deleting a user who still owns apps.",
		Args: cobra.ExactArgs(2),
		RunE: runAppsTransfer,
	}
}

func runAppsTransfer(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug, username := args[0], args[1]
	body, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/owner", bytes.NewReader(body))
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
		return httpError(cfg.Token, "transfer ownership", resp, out)
	}
	return renderAction(cmd, "transferred",
		map[string]any{"slug": slug, "owner": username},
		fmt.Sprintf("%s: ownership transferred to %s", slug, username))
}

// ── tokens create ───────────────────────────────────────────────────────────

type tokensCreateFlags struct {
	name          string
	format        string
	expiresInDays int
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
	cmd.Flags().IntVar(&f.expiresInDays, "expires-in-days", 90, "Days until the token expires (1-365; default 90)")
	return cmd
}

// tokenCreateResult holds the fields returned by the server on token creation.
type tokenCreateResult struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Token     string  `json:"token"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
}

func runTokensCreate(cmd *cobra.Command, args []string, f *tokensCreateFlags) error {
	switch f.format {
	case "text", "json":
		// valid
	default:
		return validationErr(fmt.Sprintf("--format must be %q or %q, got %q", "text", "json", f.format), "")
	}
	// --format text/json is a legacy selector that maps onto the global output
	// format. Treat --format json like --json (legacy alias) and --format text
	// like --output table (explicit table request). Conflicts follow the same
	// rule as --json vs --output table: error on mismatch.
	format, err := resolveLegacyTextJSON(f.format)
	if err != nil {
		return err
	}

	if f.expiresInDays < 1 || f.expiresInDays > 365 {
		return validationErr("--expires-in-days must be between 1 and 365", "")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	payload := map[string]any{"name": f.name, "expires_in_days": f.expiresInDays}
	body, err := json.Marshal(payload)
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
		out, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "create token", resp, out)
	}
	var result tokenCreateResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if format == formatJSON {
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
		// confirmation. When stdin is not a TTY (CI, cron, `< /dev/null`,
		// piped scripts) refuse with a structured error that names --yes so
		// automation has a clear, actionable path.
		if !isStdinTTY() {
			return confirmationRequiredError(
				"apps delete requires interactive confirmation",
				"--yes")
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
	if resp.StatusCode == http.StatusNotFound {
		// The app does not exist; the desired outcome (absent) is already in
		// place. Write a notice to stderr so a typo'd slug is visible to the
		// caller, then exit 0 with status "absent".
		fmt.Fprintf(cmd.ErrOrStderr(), "note: app %q did not exist\n", slug)
		return renderAction(cmd, "absent",
			map[string]any{"slug": slug},
			fmt.Sprintf("%s: already absent", slug))
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "delete app", resp, out)
	}
	return renderAction(cmd, "deleted",
		map[string]any{"slug": slug},
		fmt.Sprintf("%s: deleted", slug))
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
		return httpError(cfg.Token, "stop app", resp, out)
	}
	return renderAction(cmd, "stopped",
		map[string]any{"slug": slug},
		fmt.Sprintf("%s: stopped", slug))
}

// ── apps sleep ──────────────────────────────────────────────────────────────

func newAppsSleepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep <slug>",
		Short: "Put a running app to sleep (wakes on the next request)",
		Long: "Put a running app to sleep. The app releases its resources and the next\n" +
			"request wakes it transparently, so visitors see no error page.\n\n" +
			"Use `apps stop` instead to take an app out of service until it is\n" +
			"explicitly started again.\n\n" +
			"Only apps using the default multiplex worker isolation can sleep.",
		Args: cobra.ExactArgs(1),
		RunE: runAppsSleep,
	}
}

func runAppsSleep(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/sleep", nil)
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
		return httpError(cfg.Token, "sleep app", resp, out)
	}
	return renderAction(cmd, "hibernated",
		map[string]any{"slug": slug},
		fmt.Sprintf("%s: sleeping", slug))
}

// ── apps deployments ────────────────────────────────────────────────────────

func newAppsDeploymentsCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "deployments <slug>",
		Short: "List deployment history for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDeployments(cmd, args, f)
		},
	}
	addListFlags(cmd, f)
	return cmd
}

func runAppsDeployments(cmd *cobra.Command, args []string, f *listFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	deployments, total, err := getPaginatedList(cfg, "list deployments", "/api/apps/"+slug+"/deployments", f)
	if err != nil {
		return err
	}
	return renderServerList(cmd, f, deployments, total, nil, func(w io.Writer, items []map[string]any) {
		if len(items) == 0 {
			fmt.Fprintln(w, "No deployments.")
			return
		}
		t := newTable("ID", "VERSION", "STATUS", "CREATED").alignRight(0)
		for _, d := range items {
			created := fmt.Sprintf("%v", d["created_at"])
			if len(created) > 19 {
				created = created[:19]
			}
			t.row(dimTxt(d["id"]), txt(d["version"]), statusTxt(d["status"]), dimTxt(created))
			// Surface why a failed deploy failed, indented under its row, so the
			// cause is visible without re-querying or passing --fields.
			reason, _ := d["failure_reason"].(string)
			t.note(reason)
		}
		t.render(w)
	})
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
		return nil, httpError(cfg.Token, "list tokens", resp, out)
	}
	// The server returns the standard {items,...} list envelope. This path wants
	// the full set (to resolve a token by name), so it does not paginate.
	var env struct {
		Items []tokenInfo `json:"items"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return env.Items, nil
}

type tokensListFlags struct {
	listFlags
	all bool
}

func newTokensListCmd() *cobra.Command {
	f := &tokensListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your API tokens",
		Long: "List your API tokens.\n\n" +
			"With --all (admin only), list every token on the server with its\n" +
			"owning user - the credential inventory for revoking stale or\n" +
			"orphaned tokens (revoke by id with `shinyhub tokens revoke`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokensList(cmd, f)
		},
	}
	addListFlags(cmd, &f.listFlags)
	cmd.Flags().BoolVar(&f.all, "all", false, "List every user's tokens (admin only)")
	return cmd
}

// tokenTimeCell renders a nullable ISO timestamp map value as a fixed-width
// cell: "-" for null/absent, else the timestamp truncated to seconds.
func tokenTimeCell(v any) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return "-"
	}
	if len(s) > 19 {
		s = s[:19]
	}
	return s
}

func runTokensList(cmd *cobra.Command, f *tokensListFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	path := "/api/tokens"
	if f.all {
		path += "?all=1"
	}
	tokens, total, err := getPaginatedList(cfg, "list tokens", path, &f.listFlags)
	if err != nil {
		return err
	}
	return renderServerList(cmd, &f.listFlags, tokens, total, nil, func(w io.Writer, items []map[string]any) {
		if len(items) == 0 {
			fmt.Fprintln(w, "No tokens.")
			return
		}
		headers := []string{"ID", "NAME", "CREATED", "EXPIRES", "LAST USED"}
		if f.all {
			headers = []string{"ID", "USER", "NAME", "CREATED", "EXPIRES", "LAST USED"}
		}
		tbl := newTable(headers...).alignRight(0)
		for _, t := range items {
			row := []cell{dimTxt(t["id"])}
			if f.all {
				row = append(row, txt(t["username"]))
			}
			row = append(row, txt(t["name"]),
				dimTxt(tokenTimeCell(t["created_at"])),
				dimTxt(tokenTimeCell(t["expires_at"])),
				dimTxt(tokenTimeCell(t["last_used_at"])))
			tbl.row(row...)
		}
		tbl.render(w)
	})
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
		return httpError(cfg.Token, "revoke token", resp, out)
	}
	return renderAction(cmd, "revoked",
		map[string]any{"token_id": tokenID},
		fmt.Sprintf("token %s: revoked", tokenID))
}
