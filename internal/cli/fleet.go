package cli

import "github.com/spf13/cobra"

// newFleetCmd builds a fresh `fleet` command tree each call (no package-level
// state), mirroring newAppsCmd. plan, apply, and status are wired up; init is
// registered when implemented.
func newFleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Declaratively reconcile a fleet of apps from a manifest",
		Long: "fleet manages a set of apps from a single shinyhub-fleet.toml.\n\n" +
			"  shinyhub fleet plan     preview the diff (read-only)\n" +
			"  shinyhub fleet apply    converge: deploy changed, reconcile, prune\n\n" +
			"Example:\n" +
			"  shinyhub fleet plan -f shinyhub-fleet.toml --detailed-exitcode\n" +
			"  shinyhub fleet apply -f shinyhub-fleet.toml --prune --yes",
	}
	cmd.AddCommand(newFleetPlanCmd())
	cmd.AddCommand(newFleetApplyCmd())
	cmd.AddCommand(newFleetStatusCmd())
	return cmd
}
