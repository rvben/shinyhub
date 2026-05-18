package cli

import (
	"fmt"
	"os"
	"path/filepath"

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

	// Network + diff + render are added in Tasks 8-9. For now, prove pre-flight
	// passed without performing any mutation.
	_ = m
	fmt.Fprintln(out, "pre-flight ok (diff rendering added in a later task)")
	return nil
}
