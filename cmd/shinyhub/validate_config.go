package main

import (
	"fmt"
	"os"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/spf13/cobra"
)

var validateConfigCmd = &cobra.Command{
	Use:   "validate-config",
	Short: "Validate server configuration without starting the server",
	Long: "Load and validate server configuration using the same defaults and environment\n" +
		"overrides as serve. Does not start listeners, open the database, or modify state.\n" +
		"Run with the service's environment, working directory, and file permissions.\n\n" +
		"An explicitly selected config file must exist. With no explicit path, a missing\n" +
		"./shinyhub.yaml permits environment-only configuration, as with serve.\n" +
		"Success does not verify DNS, TLS, SSO, database connectivity, or runtime readiness.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		validationError := func(err error) error {
			return &cli.ExitCodeError{Code: 1, Kind: cli.KindValidation, Err: fmt.Errorf("load config: %w", err)}
		}
		path := serverConfigPath()
		// Load intentionally allows a missing default file for environment-only
		// deployments. A preflight of an explicitly selected file must not give
		// false reassurance when that file was misspelled or is not mounted.
		if configPath != "" || os.Getenv("SHINYHUB_CONFIG") != "" || cmd.Flags().Changed("config") {
			if _, err := os.Stat(path); err != nil {
				return validationError(err)
			}
		}
		if _, err := config.Load(path); err != nil {
			return validationError(err)
		}
		return cli.RenderAction(cmd, "valid", map[string]any{"valid": true}, "server configuration is valid")
	},
}
