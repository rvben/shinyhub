package cli

import (
	"fmt"
	"strings"

	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/spf13/cobra"
)

var localScheduleFlagNames = []string{"file", "app", "all", "standalone", "env", "env-file", "data-dir", "state-dir", "no-sync", "fresh"}

type localScheduleFlags struct {
	file            string
	apps            []string
	all, standalone bool
	run             localRunFlags
}

func configureLocalScheduleFlags(cmd *cobra.Command, f *localScheduleFlags, local *bool) {
	cmd.Flags().BoolVar(local, "local", false, "Execute a manifest schedule locally without a server")
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Local fleet manifest (auto-discovered)")
	cmd.Flags().StringArrayVar(&f.apps, "app", nil, "Local fleet app (repeatable)")
	cmd.Flags().BoolVar(&f.all, "all", false, "Require all fleet sources to be local")
	cmd.Flags().BoolVar(&f.standalone, "standalone", false, "Ignore an enclosing fleet")
	cmd.Flags().BoolVar(&f.run.noSync, "no-sync", false, "Skip dependency preparation")
	cmd.Flags().BoolVar(&f.run.fresh, "fresh", false, "Rebuild generated state; preserve app data")
	cmd.Flags().StringArrayVar(&f.run.env, "env", nil, "Extra KEY=VALUE environment variable (repeatable)")
	cmd.Flags().StringVar(&f.run.envFile, "env-file", "", "Environment file (default: app directory .env)")
	cmd.Flags().StringVar(&f.run.dataDir, "data-dir", "", "Host path for durable app data")
	cmd.Flags().StringVar(&f.run.stateDir, "state-dir", "", "Directory for generated workspace state")
}

func runLocalSchedule(cmd *cobra.Command, args []string, f *localScheduleFlags) error {
	if strings.TrimSpace(args[0]) == "" {
		return validationErr("schedule name must not be empty", "name a schedule from shinyhub.toml")
	}
	if err := rejectLocalRunGlobalFlags(cmd); err != nil {
		return err
	}
	dir := "."
	if len(args) > 1 {
		dir = args[1]
	}
	scope, err := resolveDevScope(dir, f.file, cmd.Flags().Changed("file"), f.standalone, f.all, f.apps)
	if err != nil {
		return err
	}
	f.run.scheduleName = args[0]
	if !scope.fleet() {
		slug, err := resolveLocalRunSlug(dir, "")
		if err != nil {
			return err
		}
		return executeLocalRun(cmd, dir, slug, &f.run, nil)
	}
	if len(scope.SkippedGit) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: skipping git-backed apps: %v\n", scope.SkippedGit)
	}
	configured := withFleetDevDefaults(f.run, scope.Dev, true)
	f.run = configured
	// Catch deterministic fleet configuration errors before any producer runs.
	for _, target := range scope.Targets {
		combined, err := resolveLocalRunEnvironment(target.Dir, &f.run)
		if err != nil {
			return fmt.Errorf("%s: %w", target.Slug, err)
		}
		if err := localrun.ValidateSchedule(localrun.Options{
			BundleDir: target.Dir, ManifestPath: target.Manifest,
			BundleInputs: target.BundleInputs, ScheduleName: args[0], Env: combined,
		}); err != nil {
			return &ExitCodeError{Code: 1, Kind: KindValidation, Err: fmt.Errorf("%s: %w", target.Slug, err)}
		}
	}
	// Run sequentially so fleet producers never compete for external resources
	// merely because dev normally boots its apps concurrently.
	for _, target := range scope.Targets {
		fmt.Fprintf(cmd.OutOrStdout(), "==> app %s\n", target.Slug)
		run := f.run
		run.dataDir = fleetChildPath(f.run.dataDir, target.Slug, len(scope.Targets))
		run.stateDir = fleetChildPath(f.run.stateDir, target.Slug, len(scope.Targets))
		if err := executeLocalRun(cmd, target.Dir, target.Slug, &run, func(o *localrun.Options) {
			o.ManifestPath = target.Manifest
			o.BundleInputs = target.BundleInputs
		}); err != nil {
			return fmt.Errorf("%s: %w", target.Slug, err)
		}
	}
	return nil
}
