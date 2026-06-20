package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rvben/shinyhub/internal/localrun"
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
		slug     string
		open     bool
		check    bool
	)

	cmd := &cobra.Command{
		Use:   "run [dir]",
		Short: "Run a Shiny app bundle locally in the foreground",
		Long: `Boot a Shiny app bundle on localhost without a ShinyHub server.

The bundle directory defaults to the current directory ("."). The command
resolves the same launch plan the hub would use (uv/renv dep prep, framework
flags, hot reload), streams all app output to the terminal, and shuts down
cleanly on Ctrl-C.

Use --check to run a preflight: boot, verify the app becomes healthy, then
stop and exit 0 (or 1 on failure). Suitable for CI pre-deploy smoke tests.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}

			// Resolve env vars from --env-file. If the user passed an explicit
			// path that does not exist, error. If not passed, default to
			// <dir>/.env and load it only when present (missing default is fine).
			var fileEnv []string
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
				} else if !os.IsNotExist(err) {
					return &ExitCodeError{Code: 1, Kind: KindValidation,
						Err: fmt.Errorf("default .env file: %w", err)}
				}
			}

			// Combine file env (lower priority) with --env flags (higher priority).
			combined := append(fileEnv, env...)

			// Default slug to the dir's base name.
			effectiveSlug := slug
			if effectiveSlug == "" {
				abs, err := filepath.Abs(dir)
				if err != nil {
					abs = dir
				}
				effectiveSlug = filepath.Base(abs)
			}

			opts := localrun.Options{
				BundleDir: dir,
				Slug:      effectiveSlug,
				DataDir:   dataDir,
				Port:      port,
				Env:       combined,
				NoSync:    noSync,
				NoReload:  noReload,
				Open:      open,
				Check:     check,
			}

			// The root command is invoked via Execute() (not ExecuteContext), so
			// cmd.Context() returns context.Background() without signal handling.
			// Wire SIGINT/SIGTERM here so Ctrl-C tears down the child cleanly.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if err := localrun.Run(ctx, opts, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return &ExitCodeError{Code: 1, Kind: KindInternal, Err: err}
			}
			return nil
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", 0, "Local TCP port to bind (0 = auto-allocate)")
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "Skip dep-prep steps (uv sync / renv restore)")
	cmd.Flags().BoolVar(&noReload, "no-reload", false, "Disable framework hot reload and file-watch restart")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Extra environment variables in KEY=VALUE form (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Load environment variables from a file (default: <dir>/.env if present)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Host path for app data dir (default: <dir>/.shinyhub-run/data)")
	cmd.Flags().StringVar(&slug, "slug", "", "Human label used in log output (default: dir base name)")
	cmd.Flags().BoolVar(&open, "open", false, "Open the serving URL in the default browser after readiness")
	cmd.Flags().BoolVar(&check, "check", false, "Preflight mode: boot, verify healthy, stop, exit 0/1")

	return cmd
}

// readRunEnvFile reads a file of KEY=VALUE lines (ignoring blank lines and
// lines starting with '#') and returns them as a slice of raw KEY=VALUE strings
// suitable for os/exec environment passing. It returns the raw os.Open error if
// the file does not exist, so callers can distinguish "file missing" from
// "file malformed".
func readRunEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}
