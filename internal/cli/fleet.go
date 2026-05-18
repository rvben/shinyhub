package cli

import "github.com/spf13/cobra"

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
	return cmd
}
