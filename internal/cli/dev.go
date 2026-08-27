package cli

import (
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// devFlags deliberately exposes only the controls that belong in an
// interactive development loop. The lower-level run and deploy commands keep
// their specialist preflight, Git, and one-shot deployment options.
type devFlags struct {
	remote string
	slug   string
	open   bool

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
	devCommonFlags       = []string{"help", "open", "remote", "slug"}
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

Add --remote <host> when development genuinely depends on a remote host's data,
identity, network, runtime, or compute. The value is a saved ShinyHub host name
or a server URL and must be supplied explicitly; the saved current host and
SHINYHUB_HOST never turn a local command into a remote mutation. Remote mode
attaches to an existing app by default. Use --create for a new persistent app
or --ephemeral for a private app removed after --ttl.

Both modes perform an initial start, watch the local source, coalesce save
bursts, preserve the last healthy app after a failed change, and stop cleanly
with Ctrl-C. Use --open to launch the app after its first healthy start.
Remote mode supports --output ndjson for a machine-readable event stream.

Examples:
  shinyhub dev .
  shinyhub dev . --open
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
	cmd.Flags().StringVar(&f.slug, "slug", "", "App slug (default: sanitized directory name)")
	cmd.Flags().BoolVar(&f.open, "open", false, "Open the app after its first healthy start")

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
	if remote == "" {
		return runLocalDev(cmd, args, f)
	}
	return runRemoteDev(cmd, args, f, remote)
}

func runLocalDev(cmd *cobra.Command, args []string, f *devFlags) error {
	if changed := changedDevFlags(cmd, devRemoteOnlyFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" require remote development", "add --remote <host> or remove the remote-only flags")
	}
	if changed := changedDevFlags(cmd, devServerGlobalFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" apply only to remote development", "add --remote <host> or remove the server option")
	}
	dir := devSourceDir(args)
	warnFleetCompositionOmission(cmd.ErrOrStderr(), dir, fleetOmissionRun, false)
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

func runRemoteDev(cmd *cobra.Command, args []string, f *devFlags, remote string) error {
	if changed := changedDevFlags(cmd, devLocalOnlyFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" apply only to local development", "remove the local-only flags or omit --remote")
	}
	previousHost := hostFlagOverride
	hostFlagOverride = remote
	defer func() { hostFlagOverride = previousHost }()
	deploy := &deployFlags{
		slug: f.slug, waitTimeout: f.waitTimeout, visibility: f.visibility,
		waitForServer: f.waitForServer, open: f.open,
		watch: true, watchDelay: f.watchDelay, allowRepeatedHooks: f.allowRepeatedHooks,
		create: f.create, ephemeral: f.ephemeral, ttl: f.ttl,
	}
	return runDeployWatch(cmd, args, deploy)
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
