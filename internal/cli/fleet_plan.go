package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

type fleetPlanFlags struct {
	file             string
	detailedExitcode bool
	failOnChanges    bool
	jsonOutput       bool
	noColor          bool
	waitForServer    time.Duration
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
			"  2  --detailed-exitcode / --fail-on-changes only: changes are pending\n" +
			"  3  transport / auth error\n" +
			"  6  server not ready (reachable host, but shinyhub is not up yet)\n\n" +
			"--fail-on-changes is an alias for --detailed-exitcode for CI gates.\n\n" +
			"Example:\n" +
			"  shinyhub fleet plan --fail-on-changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetPlan(cmd, f)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Path to the fleet manifest")
	cmd.Flags().BoolVar(&f.detailedExitcode, "detailed-exitcode", false, "Exit 2 when changes are pending, 0 when none")
	cmd.Flags().BoolVar(&f.failOnChanges, "fail-on-changes", false, "Alias for --detailed-exitcode: exit 2 when changes are pending (CI gate)")
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Emit machine-readable JSON")
	cmd.Flags().BoolVar(&f.noColor, "no-color", false, "Disable ANSI color (glyphs/words remain)")
	cmd.Flags().DurationVar(&f.waitForServer, "wait-for-server", 0, "Poll /api/server-info until the server is ready (e.g. 2m) before proceeding")
	return cmd
}

func runFleetPlan(cmd *cobra.Command, f *fleetPlanFlags) error {
	// --fail-on-changes is a CI-friendly alias for --detailed-exitcode.
	if f.failOnChanges {
		f.detailedExitcode = true
	}
	// fleet plan is a document command; NDJSON is not a valid output mode.
	// -o json behaves like --json (both select the machine-readable envelope).
	if format, err := resolveFormat(f.jsonOutput, false); err != nil {
		return err
	} else if format == formatJSON {
		f.jsonOutput = true
	}
	f.file = resolveFleetManifest(cmd, f.file, cmd.ErrOrStderr())
	pf, err := fleetPreflight(f.file, cmd.ErrOrStderr(), "plan", f.waitForServer)
	if err != nil {
		return err
	}
	defer pf.cleanup()
	return renderFleetPlan(cmd, f, "shinyhub fleet plan", pf.manifest, pf.host, pf.caps, pf.diff)
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
		return nil, httpError(cfg.Token, "list apps", resp, body)
	}
	// The server returns the standard {items,...} list envelope; fleet wants the
	// full set (reconciliation), so it does not paginate and tolerates a bare
	// array for resilience across server versions.
	var env struct {
		Items []db.App `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Items != nil {
		return env.Items, nil
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

// fetchServerCaps reads GET /api/server-info (unauthenticated) and returns just
// the advertised capabilities. Best-effort: an older server without the
// endpoint, an unreachable host, or a non-shinyhub response all yield zero-value
// caps (all false). fleet apply uses these capabilities to choose its
// convergence strategy, while fleet plan only records them.
func fetchServerCaps(cfg *cliConfig) serverCaps {
	info, err := probeServer(cfg)
	if err != nil {
		return serverCaps{}
	}
	return info.Capabilities
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
