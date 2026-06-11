package cli

import (
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

type fleetApplyFlags struct {
	file                     string
	prune                    bool
	adopt                    bool
	dryRun                   bool
	yes                      bool
	allowUnsafeDegradedPrune bool
	noColor                  bool
	jsonOutput               bool
	retries                  int
	healthTimeout            int
	waitForWarm              bool
	waitForServer            time.Duration
}

func newFleetApplyCmd() *cobra.Command {
	f := &fleetApplyFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge the fleet: deploy changed, reconcile config, optionally prune",
		Long: "Recomputes the same diff as 'fleet plan' (a prior plan is never\n" +
			"replayed), then converges: deploys changed apps, reconciles\n" +
			"fleet-declared config drift, stamps ownership, and - only with\n" +
			"--prune - removes fleet-owned apps absent from the manifest\n" +
			"(this also removes their persistent data directory). Non-atomic,\n" +
			"continue-on-error, per-app retry.\n\n" +
			"Exit codes:\n" +
			"  0  all converged (or --dry-run report)\n" +
			"  1  usage / manifest validation error\n" +
			"  3  transport / auth error\n" +
			"  4  partial: >=1 app failed after retries\n" +
			"  5  conflicts: >=1 app skipped on a precondition 409\n" +
			"  6  server not ready (reachable host, but shinyhub is not up yet)\n\n" +
			"Example:\n" +
			"  shinyhub fleet apply -f shinyhub-fleet.toml --prune --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetApply(cmd, f)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Path to the fleet manifest")
	cmd.Flags().BoolVar(&f.prune, "prune", false, "Delete fleet-owned apps absent from the manifest (removes data dir)")
	cmd.Flags().BoolVar(&f.adopt, "adopt", false, "Take ownership of in-scope apps not yet fleet-managed")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Show the plan and make no changes (identical to 'fleet plan')")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "Skip the destructive-action confirmation prompt")
	cmd.Flags().BoolVar(&f.allowUnsafeDegradedPrune, "allow-unsafe-degraded-prune", false,
		"Allow prune against a server without precondition support (accepts a documented race)")
	cmd.Flags().BoolVar(&f.noColor, "no-color", false, "Disable ANSI color (glyphs/words remain)")
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Emit the machine-readable JSON envelope")
	cmd.Flags().IntVar(&f.retries, "retries", 1, "Retry attempts after the first for deploy-bearing actions")
	cmd.Flags().IntVar(&f.healthTimeout, "health-timeout", 120, "Seconds to wait per app for healthy status after deploy")
	cmd.Flags().BoolVar(&f.waitForWarm, "wait-for-warm", false, "Wait for run_on_register first-fires to finish (within --health-timeout); a genuine failure fails that app")
	cmd.Flags().DurationVar(&f.waitForServer, "wait-for-server", 0, "Poll /api/server-info until the server is ready (e.g. 2m) before proceeding")
	return cmd
}

func runFleetApply(cmd *cobra.Command, f *fleetApplyFlags) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	pf, err := fleetPreflight(f.file, errOut, "apply", f.waitForServer)
	if err != nil {
		return err
	}
	defer pf.cleanup()

	if f.dryRun {
		synthetic := &fleetPlanFlags{
			file:       f.file,
			noColor:    f.noColor,
			jsonOutput: f.jsonOutput,
		}
		return renderFleetPlan(cmd, synthetic, "shinyhub fleet apply --dry-run", pf.manifest, pf.host, pf.caps, pf.diff)
	}

	if w := foreignAdoptWarning(pf.diff, f.adopt); w != "" {
		fmt.Fprintln(errOut, w)
	}

	degraded := !pf.caps.FleetPreconditions
	if degraded {
		fmt.Fprintln(errOut, "warning: server does not support fleet preconditions; "+
			"config patches use a re-GET fallback (smaller TOCTOU window) and prune "+
			"is disabled unless --allow-unsafe-degraded-prune is set.")
	}

	// Destructive confirmation: only when prune candidates exist AND prune
	// will actually run (preconditions present, or the unsafe override).
	if f.prune {
		var candidates []string
		for _, d := range pf.diff {
			if d.Action == fleet.ActionDelete {
				candidates = append(candidates, d.Slug)
			}
		}
		willPrune := !degraded || f.allowUnsafeDegradedPrune
		if len(candidates) > 0 && willPrune && !f.yes {
			invocation := "shinyhub fleet apply --prune --yes"
			if f.file != defaultFleetManifest {
				invocation = "shinyhub fleet apply --prune --yes -f " + shellQuote(f.file)
			}
			if !isStdinTTY() {
				fmt.Fprintf(errOut, "--prune needs interactive confirmation; re-run non-interactively with: %s\n", invocation)
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("--prune in non-interactive shell requires --yes"), Reported: true}
			}
			fmt.Fprintf(errOut,
				"This will PERMANENTLY delete %d fleet-owned app(s) and their persistent\n"+
					"data directories and all bundles: %v\nType the word 'prune' to confirm: ",
				len(candidates), candidates)
			var confirm string
			if _, serr := fmt.Fscan(cmd.InOrStdin(), &confirm); serr != nil {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("read confirmation: %w", serr)}
			}
			if confirm != "prune" {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("confirmation did not match 'prune' - aborted")}
			}
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return &ExitCodeError{Code: 3, Err: err}
	}

	opt := convergeOpts{
		adopt:              f.adopt,
		prune:              f.prune,
		allowDegradedPrune: f.allowUnsafeDegradedPrune,
		preconditions:      pf.caps.FleetPreconditions,
		retries:            f.retries,
		healthTimeout:      healthTimeoutDuration(f.healthTimeout),
		waitForWarm:        f.waitForWarm,
		fleetID:            pf.manifest.FleetID,
		runID:              newRunID(),
	}
	// Per-app deploy progress (zip summary, health-wait lines) is diagnostic,
	// not the report. In --json mode it must not pollute stdout, which has to
	// stay a single parseable envelope, so it is routed to stderr.
	progressOut := out
	if f.jsonOutput {
		progressOut = errOut
	}
	results := convergeFleet(cfg, pf, opt, progressOut)

	if f.jsonOutput {
		code, reason := applyExitCode(results)
		if jerr := writeFleetApplyJSON(out, pf.manifest, pf.host, pf.diff, results, code, reason); jerr != nil {
			return &ExitCodeError{Code: 1, Err: jerr}
		}
		return applyExitErr(code, reason)
	}
	return renderApplyReport(out, pf.manifest.FleetID, results, quietFlag)
}
