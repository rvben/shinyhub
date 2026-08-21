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
	var (
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
	)

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

Use --check to run a preflight: boot, verify the app becomes healthy, then
stop and exit 0 (or 1 on failure). Suitable for CI pre-deploy smoke tests.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range []string{"config", "host", "output", "quiet", "no-color"} {
				if cmd.Flags().Changed(name) {
					return &ExitCodeError{Code: 1, Kind: KindValidation,
						Err: fmt.Errorf("--%s does not apply to local app runs", name)}
				}
			}
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			warnFleetCompositionOmission(cmd.ErrOrStderr(), dir, fleetOmissionRun, false)

			// Resolve env vars from --env-file. If the user passed an explicit
			// path that does not exist, error. If not passed, default to
			// <dir>/.env and load it only when present (missing default is fine).
			var fileEnv []envFileEntry
			if envFile != "" {
				// Explicit path: error if missing.
				fe, err := readRunEnvFile(envFile)
				if err != nil {
					return &ExitCodeError{Code: 1, Kind: KindValidation,
						Err: fmt.Errorf("--env-file %q: %w", envFile, err)}
				}
				fileEnv = fe
			} else {
				// Default: <dir>/.env, silently skip if absent.
				defaultEnvFile := filepath.Join(dir, ".env")
				if fe, err := readRunEnvFile(defaultEnvFile); err == nil {
					fileEnv = fe
				} else if !errors.Is(err, os.ErrNotExist) {
					return &ExitCodeError{Code: 1, Kind: KindValidation,
						Err: fmt.Errorf("default .env file: %w", err)}
				}
			}

			// File values are the base layer; repeatable --env flags replace a
			// matching key. Both paths use the exact parser used by `env apply`.
			combinedEntries := append([]envFileEntry(nil), fileEnv...)
			positions := make(map[string]int, len(combinedEntries))
			for i, entry := range combinedEntries {
				positions[entry.Key] = i
			}
			for _, assignment := range env {
				parsed, err := parseEnvFile(strings.NewReader(assignment + "\n"))
				if err != nil || len(parsed) != 1 {
					if err == nil {
						err = fmt.Errorf("expected KEY=VALUE")
					}
					return &ExitCodeError{Code: 1, Kind: KindValidation,
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

			// Default slug to the dir's base name.
			effectiveSlug := slug
			if effectiveSlug == "" {
				abs, err := filepath.Abs(dir)
				if err != nil {
					abs = dir
				}
				effectiveSlug = sanitizeSlug(filepath.Base(abs))
				if effectiveSlug == "" {
					effectiveSlug = "app"
				}
			} else if !slugpkg.Valid(effectiveSlug) {
				return &ExitCodeError{Code: 1, Kind: KindValidation,
					Err: fmt.Errorf("invalid slug %q: must be %s", effectiveSlug, slugpkg.HumanRule)}
			}

			opts := localrun.Options{
				BundleDir: dir,
				Slug:      effectiveSlug,
				DataDir:   dataDir,
				StateDir:  stateDir,
				Port:      port,
				Env:       combined,
				NoSync:    noSync,
				NoReload:  noReload,
				Fresh:     fresh,
				Open:      open,
				Check:     check,
			}

			// The root command is invoked via Execute() (not ExecuteContext), so
			// cmd.Context() returns context.Background() without signal handling.
			// Wire SIGINT/SIGTERM here so Ctrl-C tears down the child cleanly.
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
		},
	}
	// Server-selection and structured-output globals do not affect a foreground
	// local process. Keep them out of this command's help instead of presenting
	// controls that are silently ignored; RunE also rejects them if supplied.
	cmd.SetUsageTemplate(`Usage:
  {{.UseLine}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}
`)

	cmd.Flags().IntVarP(&port, "port", "p", 0, "Local TCP port to bind (0 = auto-allocate)")
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "Skip dep-prep steps (uv sync / renv restore)")
	cmd.Flags().BoolVar(&noReload, "no-reload", false, "Disable staged reload when source files change")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "Rebuild generated workspace state from scratch (preserves app data)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Extra environment variables in KEY=VALUE form (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Load environment variables from a file (default: <dir>/.env if present)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Host path for app data dir (default: ShinyHub user cache)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "Local workspace/state directory (default: ShinyHub user cache keyed by app path)")
	cmd.Flags().StringVar(&slug, "slug", "", "App slug used by the local /app/<slug>/ proxy (default: sanitized dir name)")
	cmd.Flags().BoolVar(&open, "open", false, "Open the serving URL in the default browser after readiness")
	cmd.Flags().BoolVar(&check, "check", false, "Preflight mode: boot, verify healthy, stop, exit 0/1")

	return cmd
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
