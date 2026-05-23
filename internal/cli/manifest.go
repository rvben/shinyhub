package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/spf13/cobra"
)

// newManifestCmd builds the `manifest` command tree. Today it carries a single
// `validate` subcommand that parses shinyhub.toml locally so manifest typos are
// caught before a bundle is uploaded.
func newManifestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "manifest", Short: "Work with the bundle manifest (shinyhub.toml)"}
	cmd.AddCommand(newManifestValidateCmd())
	return cmd
}

func newManifestValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [dir]",
		Short: "Validate shinyhub.toml locally before deploying",
		Long: `Validate parses the bundle manifest (shinyhub.toml) the same way the
server does at deploy time and reports any error locally, so a typo or invalid
value is caught before you upload.

The manifest is optional: a directory without a shinyhub.toml validates cleanly.
With no [dir] argument, the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}

			// A missing shinyhub.toml is a valid (empty) manifest, but a
			// misspelled or nonexistent bundle path is a misuse: validating it
			// must fail rather than silently report "nothing to validate".
			info, err := os.Stat(dir)
			if err != nil {
				return fmt.Errorf("validate %s: %w", dir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("validate %s: not a directory", dir)
			}

			m, err := deploy.LoadManifest(dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if m == nil {
				fmt.Fprintf(out, "%s: no shinyhub.toml found (nothing to validate)\n", dir)
				return nil
			}

			fmt.Fprintf(out, "%s/shinyhub.toml: OK (valid)\n", dir)
			for _, line := range summarizeManifest(m) {
				fmt.Fprintf(out, "  %s\n", line)
			}
			return nil
		},
	}
}

// summarizeManifest renders a short human summary of a parsed manifest: the
// [app] overrides, each post-deploy hook, and each schedule by name.
func summarizeManifest(m *deploy.Manifest) []string {
	var lines []string

	var appParts []string
	if m.App.Replicas != nil {
		appParts = append(appParts, fmt.Sprintf("replicas=%d", *m.App.Replicas))
	}
	if m.App.MaxSessionsPerReplica != nil {
		appParts = append(appParts, fmt.Sprintf("max_sessions_per_replica=%d", *m.App.MaxSessionsPerReplica))
	}
	if m.App.HibernateResetToDefault {
		appParts = append(appParts, "hibernate_timeout=reset-to-default")
	} else if m.App.HibernateTimeoutMinutes != nil {
		appParts = append(appParts, fmt.Sprintf("hibernate_timeout_minutes=%d", *m.App.HibernateTimeoutMinutes))
	}
	if len(appParts) > 0 {
		lines = append(lines, "app: "+strings.Join(appParts, ", "))
	}

	for _, h := range m.PostDeploy() {
		lines = append(lines, fmt.Sprintf("hook (%s): %s", h.On, strings.Join(h.Command, " ")))
	}
	for _, s := range m.Schedules {
		tz := s.Timezone
		if tz == "" {
			tz = "inherit"
		}
		lines = append(lines, fmt.Sprintf("schedule %q: cron %q (tz %s)", s.Name, s.Cron, tz))
	}
	return lines
}
