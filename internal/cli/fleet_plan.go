package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

type fleetPlanFlags struct {
	file             string
	detailedExitcode bool
	jsonOutput       bool
	noColor          bool
	quiet            bool
}

func newFleetPlanCmd() *cobra.Command {
	f := &fleetPlanFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview the fleet reconcile diff (read-only, no changes)",
		Long: "Validates the manifest, resolves sources, fetches server state,\n" +
			"and prints the would-be diff. Makes only GET requests.\n\n" +
			"Exit codes:\n" +
			"  0  report printed (default), or no changes (--detailed-exitcode)\n" +
			"  1  usage / manifest validation error\n" +
			"  2  --detailed-exitcode only: changes are pending\n" +
			"  3  transport / auth error\n\n" +
			"Example:\n" +
			"  shinyhub fleet plan -f shinyhub-fleet.toml --detailed-exitcode",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetPlan(cmd, f)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", "shinyhub-fleet.toml", "Path to the fleet manifest")
	cmd.Flags().BoolVar(&f.detailedExitcode, "detailed-exitcode", false, "Exit 2 when changes are pending, 0 when none")
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Emit machine-readable JSON")
	cmd.Flags().BoolVar(&f.noColor, "no-color", false, "Disable ANSI color (glyphs/words remain)")
	cmd.Flags().BoolVarP(&f.quiet, "quiet", "q", false, "Collapse to the summary line only")
	return cmd
}

func runFleetPlan(cmd *cobra.Command, f *fleetPlanFlags) error {
	pf, err := fleetPreflight(f.file, cmd.ErrOrStderr(), "plan")
	if err != nil {
		return err
	}
	defer pf.cleanup()
	return renderFleetPlan(cmd, f, pf.manifest, pf.host, pf.caps, pf.diff)
}

// fetchApps issues the single read-only GET /api/apps the plan needs.
func fetchApps(cfg *cliConfig) ([]db.App, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps", nil)
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
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, string(body))
	}
	var apps []db.App
	if err := json.Unmarshal(body, &apps); err != nil {
		return nil, fmt.Errorf("decode apps: %w", err)
	}
	return apps, nil
}

type serverCaps struct {
	FleetPreconditions bool `json:"fleet_preconditions"`
	ContentDigest      bool `json:"content_digest"`
}

// fetchServerCaps reads GET /api/server-info (unauthenticated). Best-effort:
// an older server without the endpoint yields a zero-value caps (all false);
// fleet apply uses these capabilities to choose its convergence strategy, while
// fleet plan only records them.
func fetchServerCaps(cfg *cliConfig) serverCaps {
	var c serverCaps
	req, err := http.NewRequest("GET", cfg.Host+"/api/server-info", nil)
	if err != nil {
		return c
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c
	}
	body, _ := io.ReadAll(resp.Body)
	var wrap struct {
		Capabilities serverCaps `json:"capabilities"`
	}
	if json.Unmarshal(body, &wrap) == nil {
		c = wrap.Capabilities
	}
	return c
}

// intPtrIfPositive maps a non-pointer API int to *int, treating 0 as "unset"
// (the apps payload uses 0 for never-configured replicas/sessions, whereas
// the diff wants nil = "server has no opinion").
func intPtrIfPositive(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

