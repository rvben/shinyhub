package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
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
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	data, err := os.ReadFile(f.file)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(errOut, "no %s found. Run 'shinyhub fleet init' to generate one from your\n"+
				"deployed apps, or pass -f <path> to point at an existing manifest.\n",
				filepath.Base(f.file))
			return &ExitCodeError{Code: 1, Err: fmt.Errorf("manifest not found: %s", f.file)}
		}
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("read %s: %w", f.file, err)}
	}

	// Pre-flight step 1: manifest + local, aggregated (spec §9.1).
	m, probs := fleet.ParseManifest(data, f.file)
	if len(probs) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet plan: validating %s\n\n", f.file)
		for _, p := range probs {
			fmt.Fprintf(errOut, "  ✗ %s\n", p.Error())
		}
		fmt.Fprintf(errOut, "\n%d problem(s) found. Nothing was changed. Fix these and re-run.\n", len(probs))
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("%d manifest problem(s)", len(probs))}
	}

	// Pre-flight step 2: server reachability + auth, one call, BEFORE any
	// git clone (spec §9.1); an auth failure must cost zero clones.
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(errOut, "  ✗ not authenticated: %v\n     run 'shinyhub login' or pass --config\n", err)
		return &ExitCodeError{Code: 3, Err: err}
	}
	apps, err := fetchApps(cfg)
	if err != nil {
		fmt.Fprintf(errOut, "  ✗ cannot reach server %s: %v\n     check the URL / run 'shinyhub login'\n", cfg.Host, err)
		return &ExitCodeError{Code: 3, Err: err}
	}
	caps := fetchServerCaps(cfg) // best-effort; degraded behavior is Plan 3

	// Pre-flight step 3: resolve sources + compute local digests. Failures are
	// aggregated and reported together (spec §9.1 step 3).
	localDigests := map[string]string{}
	var resolveProblems []string
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
	for _, app := range m.Apps {
		ps, sp := fleet.ParseSource(app.Source, filepath.Dir(f.file))
		if sp != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %s", app.Slug, sp.Msg))
			continue
		}
		dir := ps.LocalPath
		if ps.Kind == fleet.SourceGit {
			gd, _, _, clean, gerr := resolveGitSource(ps)
			if gerr != nil {
				resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, gerr))
				continue
			}
			cleanups = append(cleanups, clean)
			dir = gd
		}
		dg, derr := digestLocalDir(dir)
		if derr != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, derr))
			continue
		}
		localDigests[app.Slug] = dg
	}
	if len(resolveProblems) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet plan: resolving sources\n\n")
		for _, p := range resolveProblems {
			fmt.Fprintf(errOut, "  ✗ %s\n", p)
		}
		fmt.Fprintf(errOut, "\n%d source problem(s). Nothing was changed.\n", len(resolveProblems))
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("%d source problem(s)", len(resolveProblems))}
	}

	observed := make([]fleet.ObservedApp, 0, len(apps))
	for _, a := range apps {
		observed = append(observed, fleet.ObservedApp{
			Slug:                    a.Slug,
			Access:                  a.Access,
			HibernateTimeoutMinutes: a.HibernateTimeoutMinutes,
			Replicas:                intPtrIfPositive(a.Replicas),
			MaxSessionsPerReplica:   intPtrIfPositive(a.MaxSessionsPerReplica),
			ContentDigest:           a.ContentDigest,
			ManagedBy:               a.ManagedBy,
		})
	}
	diff := fleet.Diff(m, localDigests, observed)

	// Temporary Task-8 output (replaced by the real renderer in Task 9).
	_ = caps
	for _, d := range diff {
		fmt.Fprintf(out, "%s %s\n", d.Action, d.Slug)
	}
	return nil
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
// an older server without the endpoint yields a zero-value caps (all false),
// which Plan 3 uses to choose degraded behavior. Plan 2 only records it.
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

// renderFleetPlanStubMarker is implemented in fleet_render.go (Task 9). This
// temporary stub keeps Task 8 independently compilable/committable; Task 9
// replaces it with the real renderer in a different file and deletes this stub.
func renderFleetPlanStubMarker() {}
