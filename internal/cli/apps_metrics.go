package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

type appsMetricsFlags struct {
	jsonOutput bool
}

func newAppsMetricsCmd() *cobra.Command {
	f := &appsMetricsFlags{}
	cmd := &cobra.Command{
		Use:   "metrics <slug>",
		Short: "Show live per-replica resource usage for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsMetrics(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runAppsMetrics(cmd *cobra.Command, args []string, f *appsMetricsFlags) error {
	format, err := resolveFormat(f.jsonOutput, false)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slug := args[0]
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/metrics", nil)
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
		return httpError(cfg.Token, "app metrics", resp, out)
	}

	if format == formatJSON {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	var m struct {
		Status           string `json:"status"`
		SessionsCap      int    `json:"sessions_cap"`
		WorkerIsolation  string `json:"worker_isolation"`
		MaxWorkers       int    `json:"max_workers"`
		MetricsAvailable bool   `json:"metrics_available"`
		Replicas         []struct {
			Index            int      `json:"index"`
			Status           string   `json:"status"`
			PID              *int     `json:"pid"`
			CPUPercent       *float64 `json:"cpu_percent"`
			RSSBytes         int64    `json:"rss_bytes"`
			PSSBytes         *int64   `json:"pss_bytes"`
			USSBytes         *int64   `json:"uss_bytes"`
			SwapPSSBytes     *int64   `json:"swap_pss_bytes"`
			Sessions         int      `json:"sessions"`
			Tier             string   `json:"tier"`
			Provider         string   `json:"provider"`
			MetricsAvailable bool     `json:"metrics_available"`
		} `json:"replicas"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	w := cmd.OutOrStdout()
	s := stylerFor(w)
	note := "available"
	if !m.MetricsAvailable {
		note = "unavailable"
	}
	if m.WorkerIsolation != "" {
		// Elastic pool: sessions_cap is the per-worker cap, so the honest
		// summary is the ceiling arithmetic, not the multiplex per-replica cap.
		fmt.Fprintf(w, "App: %s · %s · ceiling %d (max %d workers × %d/worker) · metrics %s\n",
			s.status(m.Status), m.WorkerIsolation, m.MaxWorkers*m.SessionsCap, m.MaxWorkers, m.SessionsCap, note)
	} else {
		fmt.Fprintf(w, "App: %s · sessions cap %d · metrics %s\n", s.status(m.Status), m.SessionsCap, note)
	}
	if len(m.Replicas) == 0 {
		fmt.Fprintln(w, "No running replicas.")
		return nil
	}
	t := newTable("REPLICA", "STATUS", "PID", "CPU%", "RSS", "PSS", "PRIVATE", "SWAP PSS", "SESSIONS", "PLACEMENT").
		alignRight(0, 2, 3, 4, 5, 6, 7, 8)
	for _, r := range m.Replicas {
		pid := "-"
		if r.PID != nil {
			pid = fmt.Sprintf("%d", *r.PID)
		}
		// A null cpu_percent means the server has no rate for this replica yet
		// (first poll after a start, or a tier that cannot report one). Render it
		// as "-" rather than 0.0, which would read as a genuinely idle replica.
		cpu := "-"
		if r.CPUPercent != nil {
			cpu = fmt.Sprintf("%.1f", *r.CPUPercent)
		}
		placement := r.Tier
		if r.Provider != "" {
			if placement != "" {
				placement += "/"
			}
			placement += r.Provider
		}
		t.row(txt(r.Index), statusTxt(r.Status), dimTxt(pid), txt(cpu),
			txt(humanBytes(r.RSSBytes)), txt(optionalBytes(r.PSSBytes)),
			txt(optionalBytes(r.USSBytes)), txt(optionalBytes(r.SwapPSSBytes)),
			txt(r.Sessions), dimTxt(placement))
	}
	t.render(w)
	return nil
}

func optionalBytes(v *int64) string {
	if v == nil {
		return "-"
	}
	return humanBytes(*v)
}
