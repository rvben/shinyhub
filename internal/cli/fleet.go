package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newFleetCmd builds a fresh `fleet` command tree each call (no package-level
// state), mirroring newAppsCmd.
func newFleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Declaratively reconcile a fleet of apps from a manifest",
		Long: "fleet manages a set of apps from a single shinyhub-fleet.toml.\n\n" +
			"  shinyhub fleet init      scaffold a manifest from deployed apps\n" +
			"  shinyhub fleet plan      preview the diff (read-only)\n" +
			"  shinyhub fleet apply     converge: deploy changed, reconcile, prune\n" +
			"  shinyhub fleet status    read-only fleet overview (no manifest)\n\n" +
			"Example:\n" +
			"  shinyhub fleet init --fleet-id prod-eu --source-root ./apps\n" +
			"  shinyhub fleet plan -f shinyhub-fleet.toml --detailed-exitcode\n" +
			"  shinyhub fleet apply -f shinyhub-fleet.toml --prune --yes",
	}
	cmd.AddCommand(newFleetInitCmd())
	cmd.AddCommand(newFleetPlanCmd())
	cmd.AddCommand(newFleetApplyCmd())
	cmd.AddCommand(newFleetStatusCmd())
	// Flag-parse errors happen before RunE, so the dedupe wrapper never sees
	// them; with SilenceErrors set on the subcommands cobra would print
	// nothing. Print them here instead. Subcommands inherit this via the
	// parent walk in (*cobra.Command).FlagErrorFunc.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(c.ErrOrStderr(), "error: %v\n", err)
		return err
	})
	ownFleetErrors(cmd)
	return cmd
}

// ownFleetErrors makes the fleet subcommands the sole owner of error printing.
// Every fleet command emits its own contextual guidance (a "✗ ..." preflight
// box, an apply report, an invalid-flag message); cobra's generic "Error: ..."
// line on top of that is noise and was reported as a duplicate. Setting
// SilenceErrors on each subcommand suppresses cobra's line, and a RunE wrapper
// prints exactly the errors that have no message of their own. Recurses so
// nested commands added later inherit the same behavior.
func ownFleetErrors(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		sub.SilenceErrors = true
		if inner := sub.RunE; inner != nil {
			sub.RunE = func(c *cobra.Command, args []string) error {
				err := inner(c, args)
				if err != nil {
					printUnlessReported(c.ErrOrStderr(), err)
				}
				return err
			}
		}
		ownFleetErrors(sub)
	}
}

// printUnlessReported writes a single "error: <msg>" line for err unless the
// command already reported it (an ExitCodeError with Reported set). Detailed
// exit-code signals and post-report apply/conflict codes are flagged Reported
// so they stay silent here while still carrying their process exit code.
func printUnlessReported(w io.Writer, err error) {
	var ece *ExitCodeError
	if errors.As(err, &ece) && ece.Reported {
		return
	}
	fmt.Fprintf(w, "error: %v\n", err)
}
