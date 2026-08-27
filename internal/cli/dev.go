package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// devFlags deliberately exposes only the controls that belong in an
// interactive development loop. The lower-level run and deploy commands keep
// their specialist preflight, Git, and one-shot deployment options.
type devFlags struct {
	remote     string
	slug       string
	open       bool
	file       string
	apps       []string
	all        bool
	standalone bool

	port     int
	noSync   bool
	env      []string
	envFile  string
	dataDir  string
	stateDir string
	fresh    bool

	create             bool
	ephemeral          bool
	ttl                time.Duration
	visibility         string
	watchDelay         time.Duration
	allowRepeatedHooks bool
	waitTimeout        int
	waitForServer      time.Duration
}

var (
	devLocalOnlyFlags  = []string{"port", "no-sync", "env", "env-file", "data-dir", "state-dir", "fresh"}
	devRemoteOnlyFlags = []string{
		"create", "ephemeral", "ttl", "visibility", "watch-delay",
		"allow-repeated-hooks", "wait-timeout", "wait-for-server",
	}
	devServerGlobalFlags = []string{"config", "output", "quiet", "no-color"}
	devCommonFlags       = []string{"all", "app", "file", "help", "open", "remote", "slug", "standalone"}
)

func init() {
	cobra.AddTemplateFunc("devFlagUsages", devFlagUsages)
}

func newDevCmd() *cobra.Command {
	f := &devFlags{}
	cmd := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Develop an app locally or on an explicit remote host",
		Long: `Develop a Shiny app with one safe, continuous workflow.

Local development is the default. ShinyHub mirrors the source into isolated
workspace state, starts the production launch plan behind a local proxy, and
stages each reload until it passes readiness. A broken save leaves the last
healthy version serving. No login or remote server is used.

Context is automatic. In an ordinary app directory, dev runs that app. Inside
a local-source app declared by the nearest fleet.toml, it uses the manifest
slug and composes that app's [[bundle_file]] inputs. At a fleet root, it starts
every local-source app and labels their output; git-backed entries are named and
skipped because a filesystem watcher needs a checkout. Use repeatable --app to
select a subset, --all to require every declared source to be watchable, or
--standalone to deliberately ignore an enclosing fleet. A fleet.toml path is
also accepted directly.

Add --remote <host> when development genuinely depends on a remote host's data,
identity, network, runtime, or compute. The value is a saved ShinyHub host name
or a server URL and must be supplied explicitly; the saved current host and
SHINYHUB_HOST never turn a local command into a remote mutation. Remote mode
attaches to an existing app by default. Use --create for a new persistent app
or --ephemeral for a private app removed after --ttl. At a fleet root, remote
mode preflights and attaches every selected app before deploying; creation is a
single-app operation so a failed command cannot leave a partial fleet.

Both modes perform an initial start, watch the local source, coalesce save
bursts, preserve the last healthy app after a failed change, and stop cleanly
with Ctrl-C. Use --open to launch the app after its first healthy start.
Remote mode supports --output ndjson for a machine-readable event stream.

Examples:
  shinyhub dev .
  shinyhub dev . --open
  shinyhub dev . --app sales-dashboard
  shinyhub dev fleet.toml --remote dev
  shinyhub dev . --standalone
  shinyhub dev . --remote dev --slug sales-dev
  shinyhub dev . --remote https://dev.example.com --create --slug sales-dev
  shinyhub dev . --remote dev --ephemeral --ttl 8h`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(cmd, args, f)
		},
	}
	// Group mode-specific controls instead of making people decode one long,
	// alphabetized list. --host stays absent because --remote is the deliberate
	// mode selector; the other inherited server controls appear under Remote.
	cmd.SetUsageTemplate(`Usage:
  {{.UseLine}}

Common flags:
{{devFlagUsages . "common" | trimTrailingWhitespaces}}

Local flags:
{{devFlagUsages . "local" | trimTrailingWhitespaces}}

Remote flags:
{{devFlagUsages . "remote" | trimTrailingWhitespaces}}
`)
	cmd.Flags().StringVar(&f.remote, "remote", "", "Develop on this saved host name or server URL instead of locally")
	cmd.Flags().StringVar(&f.slug, "slug", "", "Standalone app slug (default: sanitized directory name; fleet slugs come from the manifest)")
	cmd.Flags().BoolVar(&f.open, "open", false, "Open each selected app after its first healthy start")
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Fleet manifest (auto-discovered by default)")
	cmd.Flags().StringArrayVar(&f.apps, "app", nil, "Fleet app to develop (repeatable; a fleet root defaults to all local-source apps)")
	cmd.Flags().BoolVar(&f.all, "all", false, "Develop every app declared by the fleet; fail if any source cannot be watched")
	cmd.Flags().BoolVar(&f.standalone, "standalone", false, "Ignore an enclosing fleet and treat the directory as one app")

	cmd.Flags().IntVarP(&f.port, "port", "p", 0, "TCP port for the local proxy (0 = auto-allocate)")
	cmd.Flags().BoolVar(&f.noSync, "no-sync", false, "Skip dependency preparation (uv sync / renv restore)")
	cmd.Flags().BoolVar(&f.fresh, "fresh", false, "Rebuild generated workspace state; preserve app data")
	cmd.Flags().StringArrayVar(&f.env, "env", nil, "Extra KEY=VALUE environment variable (repeatable)")
	cmd.Flags().StringVar(&f.envFile, "env-file", "", "Environment file (default: <dir>/.env when present)")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "Host path for durable app data")
	cmd.Flags().StringVar(&f.stateDir, "state-dir", "", "Directory for generated workspace state")

	cmd.Flags().BoolVar(&f.create, "create", false, "Create a new persistent app; fail if the slug exists")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "Create a private temporary app removed after --ttl")
	cmd.Flags().DurationVar(&f.ttl, "ttl", 8*time.Hour, "Temporary app lifetime, from 15m through 7d")
	cmd.Flags().StringVar(&f.visibility, "visibility", "", "New persistent app visibility: private, shared, or public")
	cmd.Flags().DurationVar(&f.watchDelay, "watch-delay", 750*time.Millisecond, "Quiet period after the last change before deploying")
	cmd.Flags().BoolVar(&f.allowRepeatedHooks, "allow-repeated-hooks", false, "Allow manifest hooks to run after every deployable change")
	cmd.Flags().IntVar(&f.waitTimeout, "wait-timeout", 300, "Seconds to wait for the app to become healthy")
	cmd.Flags().DurationVar(&f.waitForServer, "wait-for-server", 0, "Wait for a starting ShinyHub server before the initial deploy")
	_ = cmd.RegisterFlagCompletionFunc("remote", completeSavedHosts)
	return cmd
}

func devFlagUsages(cmd *cobra.Command, group string) string {
	cmd.InitDefaultHelpFlag()
	names := devCommonFlags
	switch group {
	case "local":
		names = devLocalOnlyFlags
	case "remote":
		names = append(append([]string(nil), devRemoteOnlyFlags...), devServerGlobalFlags...)
	}
	flags := pflag.NewFlagSet("dev-"+group, pflag.ContinueOnError)
	flags.SortFlags = true
	for _, name := range names {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			flag = cmd.InheritedFlags().Lookup(name)
		}
		if flag != nil {
			flags.AddFlag(flag)
		}
	}
	return flags.FlagUsages()
}

func runDev(cmd *cobra.Command, args []string, f *devFlags) error {
	if cmd.Flags().Changed("host") {
		return validationErr("--host does not select a development mode", "replace --host <host> with --remote <host>")
	}
	remote := strings.TrimSpace(f.remote)
	if cmd.Flags().Changed("remote") && remote == "" {
		return validationErr("--remote requires a saved host name or server URL", "omit --remote for local development")
	}
	scope, err := resolveDevScope(devSourceDir(args), f.file, cmd.Flags().Changed("file"), f.standalone, f.all, f.apps)
	if err != nil {
		return err
	}
	if scope.fleet() && f.slug != "" {
		return validationErr("--slug cannot override a fleet app's manifest identity", "select the app with --app <slug>, or add --standalone to develop an independent target")
	}
	if remote == "" {
		return runLocalDev(cmd, args, f, scope)
	}
	return runRemoteDev(cmd, args, f, remote, scope)
}

func runLocalDev(cmd *cobra.Command, args []string, f *devFlags, scope *devScope) error {
	if changed := changedDevFlags(cmd, devRemoteOnlyFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" require remote development", "add --remote <host> or remove the remote-only flags")
	}
	if changed := changedDevFlags(cmd, devServerGlobalFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" apply only to remote development", "add --remote <host> or remove the server option")
	}
	if scope.fleet() {
		return runLocalFleetDev(cmd, f, scope)
	}
	dir := devSourceDir(args)
	if !f.standalone {
		warnFleetCompositionOmission(cmd.ErrOrStderr(), dir, fleetOmissionRun, false)
	}
	slug, err := resolveLocalRunSlug(dir, f.slug)
	if err != nil {
		return err
	}
	local := &localRunFlags{
		port: f.port, noSync: f.noSync, env: f.env, envFile: f.envFile,
		dataDir: f.dataDir, stateDir: f.stateDir, fresh: f.fresh,
		slug: slug, open: f.open,
	}
	return executeLocalRun(cmd, dir, slug, local, nil)
}

func runLocalFleetDev(cmd *cobra.Command, f *devFlags, scope *devScope) error {
	if len(scope.Targets) > 1 && f.port != 0 {
		return validationErr("--port selects one address but this fleet development session has multiple apps", "omit --port for automatic ports or select one app with --app <slug>")
	}
	if len(scope.SkippedGit) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: fleet %s has git-backed app(s) that cannot be watched locally: %s\n", scope.FleetID, strings.Join(scope.SkippedGit, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Fleet development\n  Fleet: %s\n  Manifest: %s\n  Apps: %s\n\n",
		scope.FleetID, scope.Manifest, strings.Join(devTargetSlugs(scope.Targets), ", "))

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	type result struct {
		slug string
		err  error
	}
	results := make(chan result, len(scope.Targets))
	var outputMu sync.Mutex
	for _, target := range scope.Targets {
		target := target
		go func() {
			child := &cobra.Command{}
			child.SetContext(ctx)
			if len(scope.Targets) > 1 {
				child.SetOut(newPrefixedWriter(&outputMu, cmd.OutOrStdout(), target.Slug))
				child.SetErr(newPrefixedWriter(&outputMu, cmd.ErrOrStderr(), target.Slug))
			} else {
				child.SetOut(cmd.OutOrStdout())
				child.SetErr(cmd.ErrOrStderr())
			}
			local := &localRunFlags{
				port: f.port, noSync: f.noSync, env: f.env, envFile: f.envFile,
				dataDir:  fleetChildPath(f.dataDir, target.Slug, len(scope.Targets)),
				stateDir: fleetChildPath(f.stateDir, target.Slug, len(scope.Targets)),
				fresh:    f.fresh, slug: target.Slug, open: f.open,
			}
			err := executeLocalRun(child, target.Dir, target.Slug, local, func(options *localrun.Options) {
				options.ManifestPath = target.Manifest
				options.BundleInputs = target.BundleInputs
			})
			results <- result{slug: target.Slug, err: err}
		}()
	}
	var first error
	for range scope.Targets {
		result := <-results
		if result.err != nil && !errors.Is(result.err, context.Canceled) && first == nil {
			first = fmt.Errorf("%s: %w", result.slug, result.err)
			cancel()
		}
	}
	return first
}

func fleetChildPath(root, slug string, targets int) string {
	if root == "" || targets == 1 {
		return root
	}
	return filepath.Join(root, slug)
}

func devTargetSlugs(targets []devTarget) []string {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.Slug)
	}
	return result
}

func runRemoteDev(cmd *cobra.Command, args []string, f *devFlags, remote string, scope *devScope) error {
	if changed := changedDevFlags(cmd, devLocalOnlyFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" apply only to local development", "remove the local-only flags or omit --remote")
	}
	previousHost := hostFlagOverride
	hostFlagOverride = remote
	defer func() { hostFlagOverride = previousHost }()
	if scope.fleet() {
		return runRemoteFleetDev(cmd, f, scope)
	}
	deploy := &deployFlags{
		slug: f.slug, waitTimeout: f.waitTimeout, visibility: f.visibility,
		waitForServer: f.waitForServer, open: f.open,
		watch: true, watchDelay: f.watchDelay, allowRepeatedHooks: f.allowRepeatedHooks,
		create: f.create, ephemeral: f.ephemeral, ttl: f.ttl,
	}
	return runDeployWatch(cmd, args, deploy)
}

func runRemoteFleetDev(cmd *cobra.Command, f *devFlags, scope *devScope) error {
	if len(scope.Targets) > 1 && (f.create || f.ephemeral) {
		return validationErr("--create and --ephemeral are intentionally single-app operations", "create the fleet with `shinyhub fleet apply -f "+shellQuote(scope.Manifest)+"`, then attach with `shinyhub dev . --remote <host>`")
	}
	if len(scope.SkippedGit) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: fleet %s has git-backed app(s) that cannot be watched: %s\n", scope.FleetID, strings.Join(scope.SkippedGit, ", "))
	}
	format, err := resolveFormat(false, true)
	if err != nil {
		return err
	}

	deployments := make([]*deployFlags, len(scope.Targets))
	for i, target := range scope.Targets {
		visibility := f.visibility
		if visibility == "" && f.create {
			visibility = target.Visibility
		}
		slug := target.Slug
		if f.ephemeral {
			slug, err = generatedDevelopmentSlugForBase(target.Slug)
			if err != nil {
				return err
			}
		}
		deployments[i] = &deployFlags{
			slug: slug, waitTimeout: f.waitTimeout, visibility: visibility,
			waitForServer: f.waitForServer, open: f.open,
			watch: true, watchDelay: f.watchDelay, allowRepeatedHooks: f.allowRepeatedHooks,
			create: f.create, ephemeral: f.ephemeral, ttl: f.ttl, format: format,
			bundleManifestRoot: filepath.Dir(target.Manifest), bundleInputs: target.BundleInputs,
			watchExternalFiles: target.ExternalFiles,
		}
		if _, _, prepErr := prepareDeploymentForFlags(target.Dir, deployments[i]); prepErr != nil {
			return fmt.Errorf("app %s: %w", target.Slug, prepErr)
		}
		if hooksErr := validateRepeatedWatchHooks(target.Dir, f.allowRepeatedHooks); hooksErr != nil {
			return fmt.Errorf("app %s: %w", target.Slug, hooksErr)
		}
	}

	// For a multi-app attach, verify the complete target set before the first
	// deployment. A typo or an unapplied fleet must not start mutating a subset.
	if len(scope.Targets) > 1 {
		cfg, cfgErr := loadDeployConfig(cmd)
		if cfgErr != nil {
			return cfgErr
		}
		if f.waitForServer > 0 {
			if _, waitErr := waitForServerReady(cfg, f.waitForServer, serverPollInterval, cmd.ErrOrStderr(), time.Now, time.Sleep); waitErr != nil {
				return &ExitCodeError{Code: 6, Err: waitErr}
			}
			for _, deploy := range deployments {
				deploy.waitForServer = 0
			}
		}
		var missing []string
		for _, deploy := range deployments {
			exists, existsErr := watchTargetExists(cfg, deploy.slug)
			if existsErr != nil {
				return existsErr
			}
			if !exists {
				missing = append(missing, deploy.slug)
			}
		}
		if len(missing) > 0 {
			return validationErr("remote fleet apps do not exist: "+strings.Join(missing, ", "), "converge them first with `shinyhub fleet apply -f "+shellQuote(scope.Manifest)+"`")
		}
	}

	if !quietFlag && format != formatNDJSON {
		fmt.Fprintf(cmd.ErrOrStderr(), "Fleet remote development\n  Fleet: %s\n  Manifest: %s\n  Apps: %s\n\n",
			scope.FleetID, scope.Manifest, strings.Join(devTargetSlugs(scope.Targets), ", "))
	}
	if len(scope.Targets) == 1 {
		return runDeployWatch(cmd, []string{scope.Targets[0].Dir}, deployments[0])
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	type result struct {
		slug string
		err  error
	}
	results := make(chan result, len(scope.Targets))
	var outputMu sync.Mutex
	for i, target := range scope.Targets {
		target, deploy := target, deployments[i]
		go func() {
			child := &cobra.Command{}
			child.SetContext(ctx)
			if format == formatNDJSON {
				child.SetOut(newAppEventWriter(&outputMu, cmd.OutOrStdout(), target.Slug))
			} else {
				child.SetOut(newPrefixedWriter(&outputMu, cmd.OutOrStdout(), target.Slug))
			}
			child.SetErr(newPrefixedWriter(&outputMu, cmd.ErrOrStderr(), target.Slug))
			results <- result{slug: target.Slug, err: runDeployWatch(child, []string{target.Dir}, deploy)}
		}()
	}
	var first error
	for range scope.Targets {
		result := <-results
		if result.err != nil && !errors.Is(result.err, context.Canceled) && first == nil {
			first = fmt.Errorf("%s: %w", result.slug, result.err)
			cancel()
		}
	}
	return first
}

func changedDevFlags(cmd *cobra.Command, names []string) []string {
	changed := make([]string, 0, len(names))
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			changed = append(changed, "--"+name)
		}
	}
	return changed
}

func devSourceDir(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}
