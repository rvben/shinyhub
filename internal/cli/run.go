package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rvben/shinyhub/internal/localrun"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

// newRunCmd returns the `shinyhub run [dir]` command, which boots a Shiny app
// bundle locally in the foreground. It requires no server login or credentials.
func newRunCmd() *cobra.Command {
	f := &localRunFlags{}
	cmd := &cobra.Command{
		Use:   "run [dir]",
		Short: "Run a Shiny app bundle locally in the foreground",
		Long: `Boot a Shiny app bundle on localhost without a ShinyHub server.

The bundle directory defaults to the current directory ("."). The command
resolves the same launch plan the hub would use, mirrors the source into a
ShinyHub-owned cache, and serves it through the production proxy route at
/app/<slug>/. The source directory is never modified. Changes are staged and
health-checked before the running app is replaced, so a broken save leaves the
last healthy version online. App output streams to the terminal and Ctrl-C
shuts everything down cleanly.

For the everyday development loop, prefer 'shinyhub dev [dir]'. This run
command remains the lower-level entry point for --check and --no-reload.

Use --check to run a preflight: boot, verify the app becomes healthy, then
stop and exit 0 (or 1 on failure). Suitable for CI pre-deploy smoke tests.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectLocalRunGlobalFlags(cmd); err != nil {
				return err
			}
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			warnFleetCompositionOmission(cmd.ErrOrStderr(), dir, fleetOmissionRun, false)
			effectiveSlug, err := resolveLocalRunSlug(dir, f.slug)
			if err != nil {
				return err
			}
			return executeLocalRun(cmd, dir, effectiveSlug, f, nil)
		},
	}
	configureLocalRunCommand(cmd, f, true)
	return cmd
}

func resolveLocalRunSlug(dir, requested string) (string, error) {
	if requested != "" {
		if !slugpkg.Valid(requested) {
			return "", &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("invalid slug %q: must be %s", requested, slugpkg.HumanRule)}
		}
		return requested, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	slug := sanitizeSlug(filepath.Base(abs))
	if slug == "" {
		slug = "app"
	}
	return slug, nil
}

type localRunFlags struct {
	port     int
	noSync   bool
	noReload bool
	env      []string
	envFile  string
	dataDir  string
	stateDir string
	fresh    bool
	slug     string
	open     bool
	check    bool
}

func configureLocalRunCommand(cmd *cobra.Command, f *localRunFlags, includeSlug bool) {
	// Server-selection and structured-output globals do not affect a foreground
	// local process. Keep them out of help instead of presenting controls that
	// are rejected by both local-run entry points.
	cmd.SetUsageTemplate(`Usage:
  {{.UseLine}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}
`)
	cmd.Flags().IntVarP(&f.port, "port", "p", 0, "Local TCP port to bind (0 = auto-allocate)")
	cmd.Flags().BoolVar(&f.noSync, "no-sync", false, "Skip dep-prep steps (uv sync / renv restore)")
	cmd.Flags().BoolVar(&f.noReload, "no-reload", false, "Disable staged reload when source files change")
	cmd.Flags().BoolVar(&f.fresh, "fresh", false, "Rebuild generated workspace state from scratch (preserves app data)")
	cmd.Flags().StringArrayVar(&f.env, "env", nil, "Extra environment variables in KEY=VALUE form (repeatable)")
	cmd.Flags().StringVar(&f.envFile, "env-file", "", "Load environment variables from a file (default: <dir>/.env if present)")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "Host path for app data dir (default: ShinyHub user cache)")
	cmd.Flags().StringVar(&f.stateDir, "state-dir", "", "Local workspace/state directory (default: ShinyHub user cache keyed by app path)")
	if includeSlug {
		cmd.Flags().StringVar(&f.slug, "slug", "", "App slug used by the local /app/<slug>/ proxy (default: sanitized dir name)")
	}
	cmd.Flags().BoolVar(&f.open, "open", false, "Open the serving URL in the default browser after readiness")
	cmd.Flags().BoolVar(&f.check, "check", false, "Preflight mode: boot, verify healthy, stop, exit 0/1")
}

func rejectLocalRunGlobalFlags(cmd *cobra.Command) error {
	for _, name := range []string{"config", "host", "output", "quiet", "no-color"} {
		if cmd.Flags().Changed(name) {
			return &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("--%s does not apply to local app runs", name)}
		}
	}
	return nil
}

func executeLocalRun(cmd *cobra.Command, dir, slug string, f *localRunFlags, configure func(*localrun.Options)) error {
	combined, err := resolveLocalRunEnvironment(dir, f)
	if err != nil {
		return err
	}
	opts := localrun.Options{
		BundleDir: dir,
		Slug:      slug,
		DataDir:   f.dataDir,
		StateDir:  f.stateDir,
		Port:      f.port,
		Env:       combined,
		NoSync:    f.noSync,
		NoReload:  f.noReload,
		Fresh:     f.fresh,
		Open:      f.open,
		Check:     f.check,
	}
	if configure != nil {
		configure(&opts)
	}

	// The root command is invoked via Execute() (not ExecuteContext), so wire
	// SIGINT/SIGTERM here to tear down the child cleanly.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := localrun.Run(ctx, opts, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		kind := KindInternal
		var validationErr *localrun.ValidationError
		if errors.As(err, &validationErr) {
			kind = KindValidation
		}
		return &ExitCodeError{Code: 1, Kind: kind, Err: err}
	}
	return nil
}

func resolveLocalRunEnvironment(dir string, f *localRunFlags) ([]string, error) {
	var fileEnv []envFileEntry
	if f.envFile != "" {
		entries, err := readRunEnvFile(f.envFile)
		if err != nil {
			return nil, &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("--env-file %q: %w", f.envFile, err)}
		}
		fileEnv = entries
	} else {
		defaultEnvFile := filepath.Join(dir, ".env")
		if entries, err := readRunEnvFile(defaultEnvFile); err == nil {
			fileEnv = entries
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("default .env file: %w", err)}
		}
	}

	combinedEntries := append([]envFileEntry(nil), fileEnv...)
	positions := make(map[string]int, len(combinedEntries))
	for i, entry := range combinedEntries {
		positions[entry.Key] = i
	}
	for _, assignment := range f.env {
		parsed, err := parseEnvFile(strings.NewReader(assignment + "\n"))
		if err != nil || len(parsed) != 1 {
			if err == nil {
				err = fmt.Errorf("expected KEY=VALUE")
			}
			return nil, &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("--env %q: %w", assignment, err)}
		}
		entry := parsed[0]
		if i, ok := positions[entry.Key]; ok {
			combinedEntries[i] = entry
		} else {
			positions[entry.Key] = len(combinedEntries)
			combinedEntries = append(combinedEntries, entry)
		}
	}
	combined := make([]string, 0, len(combinedEntries))
	for _, entry := range combinedEntries {
		combined = append(combined, entry.Key+"="+entry.Value)
	}
	return combined, nil
}

// readRunEnvFile deliberately shares env apply's parser so local and deployed
// values have identical quoting, comment, validation, and duplicate semantics.
func readRunEnvFile(path string) ([]envFileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseEnvFile(f)
}
