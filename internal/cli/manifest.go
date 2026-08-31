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
			// manifest validate is a document command; NDJSON is not valid.
			if _, err := resolveFormat(false, false); err != nil {
				return err
			}
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
	// Display metadata leads the summary: it is what the operator recognizes the
	// app by, so it belongs ahead of the sizing knobs.
	if m.App.Name != nil {
		appParts = append(appParts, fmt.Sprintf("name=%q", *m.App.Name))
	}
	if m.App.Description != nil {
		appParts = append(appParts, fmt.Sprintf("description=%q", *m.App.Description))
	}
	if m.App.Replicas != nil {
		appParts = append(appParts, fmt.Sprintf("replicas=%d", *m.App.Replicas))
	}
	if m.App.MaxSessionsPerReplica != nil {
		appParts = append(appParts, fmt.Sprintf("max_sessions_per_replica=%d", *m.App.MaxSessionsPerReplica))
	}
	if m.App.RenderSeconds != nil {
		appParts = append(appParts, fmt.Sprintf("render_seconds=%g", *m.App.RenderSeconds))
	}
	if m.App.HibernateResetToDefault {
		appParts = append(appParts, "hibernate_timeout=reset-to-default")
	} else if m.App.HibernateTimeoutMinutes != nil {
		appParts = append(appParts, fmt.Sprintf("hibernate_timeout_minutes=%d", *m.App.HibernateTimeoutMinutes))
	}
	if m.App.MemoryLimitMB != nil {
		appParts = append(appParts, fmt.Sprintf("memory_limit_mb=%d", *m.App.MemoryLimitMB))
	}
	if m.App.CPUQuotaPercent != nil {
		appParts = append(appParts, fmt.Sprintf("cpu_quota_percent=%d", *m.App.CPUQuotaPercent))
	}
	if m.App.IdentityHeaders != nil {
		appParts = append(appParts, fmt.Sprintf("identity_headers=%t", *m.App.IdentityHeaders))
	}
	if m.App.UsageIdentityMode != nil {
		appParts = append(appParts, "usage_identity_mode="+*m.App.UsageIdentityMode)
	}
	if m.App.MinWarmReplicas != nil {
		appParts = append(appParts, fmt.Sprintf("min_warm_replicas=%d", *m.App.MinWarmReplicas))
	}
	if m.App.Autoscale != nil {
		enabled := false
		if m.App.Autoscale.Enabled != nil {
			enabled = *m.App.Autoscale.Enabled
		}
		appParts = append(appParts, fmt.Sprintf("autoscale={enabled=%t,min=%d,max=%d,target=%g}",
			enabled, m.App.Autoscale.MinReplicas, m.App.Autoscale.MaxReplicas, m.App.Autoscale.Target))
	}
	if m.App.Worker != nil {
		worker := []string{}
		if m.App.Worker.Isolation != nil {
			worker = append(worker, "isolation="+*m.App.Worker.Isolation)
		}
		if m.App.Worker.GroupedSize != nil {
			worker = append(worker, fmt.Sprintf("grouped_size=%d", *m.App.Worker.GroupedSize))
		}
		if m.App.Worker.MaxWorkers != nil {
			worker = append(worker, fmt.Sprintf("max_workers=%d", *m.App.Worker.MaxWorkers))
		}
		if m.App.Worker.WarmSpares != nil {
			worker = append(worker, fmt.Sprintf("warm_spares=%d", *m.App.Worker.WarmSpares))
		}
		if m.App.Worker.MaxSessionLifetimeSecs != nil {
			worker = append(worker, fmt.Sprintf("max_session_lifetime_secs=%d", *m.App.Worker.MaxSessionLifetimeSecs))
		}
		appParts = append(appParts, "worker={"+strings.Join(worker, ",")+"}")
	}
	if m.App.Icon != nil {
		appParts = append(appParts, fmt.Sprintf("icon=%s", *m.App.Icon))
	}
	if m.App.Project != nil {
		appParts = append(appParts, fmt.Sprintf("project=%q", *m.App.Project))
	}
	if len(m.App.Command) > 0 {
		appParts = append(appParts, "command="+strings.Join(m.App.Command, " "))
	}
	if m.App.BuildTimeoutSeconds != nil {
		appParts = append(appParts, fmt.Sprintf("build_timeout_seconds=%d", *m.App.BuildTimeoutSeconds))
	}
	if m.App.StartupTimeoutSeconds != nil {
		appParts = append(appParts, fmt.Sprintf("startup_timeout_seconds=%d", *m.App.StartupTimeoutSeconds))
	}
	if m.App.ReadinessPath != "" {
		appParts = append(appParts, fmt.Sprintf("readiness_path=%q", m.App.ReadinessPath))
	}
	if m.App.ReadinessStatus != nil {
		appParts = append(appParts, fmt.Sprintf("readiness_status=%d", *m.App.ReadinessStatus))
	}
	if len(appParts) > 0 {
		lines = append(lines, "app: "+strings.Join(appParts, ", "))
	}

	for _, h := range m.PostDeploy() {
		timeout := h.Timeout.String()
		if h.Timeout == 0 {
			timeout = "default"
		}
		lines = append(lines, fmt.Sprintf("hook (%s, timeout %s): %s", h.On, timeout, strings.Join(h.Command, " ")))
	}
	for _, s := range m.Schedules {
		tz := s.Timezone
		if tz == "" {
			tz = "inherit"
		}
		state := "enabled"
		if s.Disabled {
			state = "disabled"
		}
		lines = append(lines, fmt.Sprintf("schedule %q: cron %q (tz %s, %s, overlap %s, missed %s): %s",
			s.Name, s.Cron, tz, state, s.Overlap, s.Missed, strings.Join(s.Command, " ")))
	}
	if len(m.Access.ViewerGroups) > 0 {
		lines = append(lines, "access viewer_groups: "+strings.Join(m.Access.ViewerGroups, ", "))
	}
	if len(m.Access.ManagerGroups) > 0 {
		lines = append(lines, "access manager_groups: "+strings.Join(m.Access.ManagerGroups, ", "))
	}
	if m.Tracing.Auto != nil {
		lines = append(lines, fmt.Sprintf("tracing: auto=%t", *m.Tracing.Auto))
	}
	return lines
}
