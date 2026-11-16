package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/rvben/shinyhub/internal/admission"
	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/spf13/cobra"
)

var checkOIDC bool

func init() {
	validateConfigCmd.Flags().BoolVar(&checkOIDC, "check-oidc", false, "Check OIDC discovery and issuer over the network (15s timeout)")
}

var validateConfigCmd = &cobra.Command{
	Use:   "validate-config",
	Short: "Validate server configuration without starting the server",
	Long: "Load and validate server configuration using the same defaults and environment\n" +
		"overrides as serve. Does not start listeners, open the database, or modify state.\n" +
		"Run with the service's environment, working directory, and file permissions.\n\n" +
		"An explicitly selected config file must exist. With no explicit path, a missing\n" +
		"./shinyhub.yaml permits environment-only configuration, as with serve.\n" +
		"Capacity warnings describe the machine running this command; run it on the server.\n" +
		"By default this does not verify DNS, TLS, SSO, database connectivity, or runtime readiness.\nUse --check-oidc to verify discovery and issuer; it does not test sign-in or client credentials.",
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
		cfg, err := config.Load(path)
		if err != nil {
			return validationError(err)
		}
		cores, source, warnings := inspectUpdateCPUCapacity(cfg)
		if cfg.OAuth.OIDC.IssuerURL != "" {
			callback, _ := url.Parse(cfg.OAuth.OIDC.CallbackURL)
			base, _ := url.Parse(cfg.Server.BaseURL)
			if callback != nil && base != nil && base.Host != "" && callback.Host != base.Host {
				warnings = append(warnings, "OIDC callback_url host differs from server.base_url; verify the registered redirect URI")
			}
		}
		if checkOIDC {
			o := cfg.OAuth.OIDC
			if o.IssuerURL == "" {
				return validationError(fmt.Errorf("OIDC is not configured"))
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			provider, err := oauth.NewOIDCProvider(ctx, o.IssuerURL, o.ClientID, o.ClientSecret, o.CallbackURL, o.DisplayName, o.GroupsClaim, o.GroupsScope)
			if err != nil {
				return validationError(fmt.Errorf("OIDC discovery/issuer verification failed: %w", err))
			}
			if o.EndSession {
				if _, err := provider.LogoutURL("", ""); err != nil {
					return validationError(err)
				}
			}
		}
		message := "server configuration is valid"
		for _, warning := range warnings {
			message += "\nWarning: " + warning
		}
		return cli.RenderAction(cmd, "valid", map[string]any{"valid": true, "warnings": warnings, "effective_cpu_cores": cores, "cpu_capacity_source": source}, message)
	},
}

// Use the same effective quota clamp as live-update admission, including when
// host_capacity_cores overrides autodetection. No pressure sample is needed.
func inspectUpdateCPUCapacity(cfg *config.Config) (float64, string, []string) {
	detected, source := admission.Detect(cfg.HostCapacityCores())
	cores, err := admission.EffectiveCPUCores(detected)
	if cores < detected {
		source = "cgroup-quota"
	}
	if err != nil {
		return 0, "unknown", []string{"live-update CPU capacity could not be detected on this machine; check CPU accounting on the server before relying on parallel updates"}
	}
	if warning := cfg.Server.UpdateCapacity.CPUWarning(cores); warning != "" {
		return cores, source, []string{warning + " (on this machine; capacity source: " + source + ")"}
	}
	return cores, source, []string{}
}
