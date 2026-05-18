package cli

import "github.com/spf13/cobra"

// newFleetCmd builds a fresh `fleet` command tree each call (no package-level
// state), mirroring newAppsCmd. Subcommands (apply, init, status) are registered
// as they are implemented; currently only plan is wired up.
func newFleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Declaratively reconcile a fleet of apps from a manifest",
		Long: "fleet manages a set of apps from a single shinyhub-fleet.toml.\n\n" +
			"  shinyhub fleet plan    preview the diff (read-only)\n\n" +
			"Example:\n" +
			"  shinyhub fleet plan -f shinyhub-fleet.toml --detailed-exitcode",
	}
	cmd.AddCommand(newFleetPlanCmd())
	return cmd
}
