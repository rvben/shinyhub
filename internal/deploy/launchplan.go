package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DepPrepStep is one host-side preparation action (EnsureProject, uv sync,
// renv restore). The runner executes each in order before launch.
type DepPrepStep struct {
	Label string
	Run   func(ctx context.Context, bundleDir string) error
}

// LaunchPlan is the canonical description of how a single app replica launches.
// It is the one source of truth shared by the server boot path and `shinyhub run`.
type LaunchPlan struct {
	AppType   string
	Manifest  *Manifest
	Command   []string
	Env       []string // launch-coupled only ("PORT"); platform/per-app env layered by the consumer
	BindHost  string
	ReadyPath string
	DepPrep   []DepPrepStep
	Timeout   time.Duration
}

// LaunchOptions are the Manager-free inputs both consumers supply. See the
// design spec section 4.2 for the PrepHostDeps vs CommandHostDeps distinction.
type LaunchOptions struct {
	CommandOverride       []string // API/explicit command; substituted but not validated; skips detection/prep/auto-instrument
	Port                  int
	Workers               int // threaded to buildCommand; currently unused there, kept for fidelity
	BindHost              string
	PrepHostDeps          bool // include dep-prep steps (pool-wide decision)
	CommandHostDeps       bool // per-tier project-mode flag for buildCommand
	AutoInstrumentDefault bool
	HonorManifestTracing  bool // apply manifest [tracing] auto override? server true, run false
	Reload                bool
	// AppEnv is the per-app env layered into dep-prep builds on top of the
	// sanitized server base (the same variables the app process will see at
	// start, e.g. private package-index credentials). The server deploy path
	// resolves it from the app's env store; `shinyhub run` passes --env/.env.
	AppEnv []string
}

// ResolveLaunch resolves how a bundle launches, mirroring resolveBootParams +
// bootReplica's command construction exactly, but without process.Manager.
func ResolveLaunch(bundleDir string, opts LaunchOptions) (*LaunchPlan, error) {
	bindHost := opts.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	m, err := LoadManifest(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	plan := &LaunchPlan{
		Manifest:  m,
		Env:       []string{fmt.Sprintf("PORT=%d", opts.Port)},
		BindHost:  bindHost,
		ReadyPath: "/",
		Timeout:   defaultHealthTimeout,
	}
	if m != nil && m.App.StartupTimeoutSeconds != nil {
		plan.Timeout = time.Duration(*m.App.StartupTimeoutSeconds) * time.Second
	}

	switch {
	case len(opts.CommandOverride) > 0:
		plan.Command = substituteCommand(opts.CommandOverride, opts.Port, bindHost)
		return plan, nil
	case m != nil && len(m.App.Command) > 0:
		if verr := validateCommandTemplate(m.App.Command); verr != nil {
			return nil, fmt.Errorf("manifest [app] command: %w", verr)
		}
		plan.Command = substituteCommand(m.App.Command, opts.Port, bindHost)
		return plan, nil
	}

	// Inferred command (Task 3 fills python/r).
	return resolveInferred(bundleDir, bindHost, m, opts, plan)
}

func resolveInferred(bundleDir, bindHost string, m *Manifest, opts LaunchOptions, plan *LaunchPlan) (*LaunchPlan, error) {
	appType := DetectAppType(bundleDir)
	plan.AppType = appType
	switch appType {
	case "python":
		if opts.PrepHostDeps {
			plan.DepPrep = []DepPrepStep{
				{
					Label: "ensure project",
					// ensure project is best-effort: a missing pyproject.toml or
					// uv init failure should not abort the run (uv sync below
					// will still catch real problems). This matches server
					// behaviour (deploy.go warns and continues on ensureProjectFn
					// failure).
					Run: func(ctx context.Context, bundleDir string) error {
						if err := ensureProjectFn(ctx, bundleDir); err != nil {
							slog.Warn("ensure project failed; continuing", "err", err)
						}
						return nil
					},
				},
				{Label: "uv sync", Run: func(ctx context.Context, dir string) error {
					return pythonSyncFn(ctx, dir, opts.AppEnv)
				}},
			}
		}
		auto := opts.AutoInstrumentDefault
		if opts.HonorManifestTracing && m != nil && m.Tracing.Auto != nil {
			auto = *m.Tracing.Auto
		}
		plan.Command = withPythonReload(buildCommandFn(bundleDir, opts.Port, opts.Workers, bindHost, auto, opts.CommandHostDeps), opts.Reload)
	case "r":
		if opts.PrepHostDeps {
			plan.DepPrep = []DepPrepStep{{Label: "renv restore", Run: func(ctx context.Context, dir string) error {
				return rSyncFn(ctx, dir, opts.AppEnv)
			}}}
		}
		plan.Command = buildRCommandReload(bundleDir, opts.Port, bindHost, opts.Reload)
	default:
		return nil, fmt.Errorf("no app.py or app.R found in %s (add one, or declare [app] command in shinyhub.toml)", bundleDir)
	}
	return plan, nil
}

// withPythonReload appends `--reload` to an inferred `shiny run` command when
// reload is requested. The flag targets `shiny run` even when the entrypoint is
// wrapped by opentelemetry-instrument (the wrapper execs shiny run).
func withPythonReload(cmd []string, reload bool) []string {
	if !reload {
		return cmd
	}
	return append(append([]string{}, cmd...), "--reload")
}

// buildRCommandReload builds the R launch command, optionally enabling Shiny's
// in-process autoreload. BuildRCommand stays the canonical no-reload builder.
func buildRCommandReload(bundleDir string, port int, bindHost string, reload bool) []string {
	if !reload {
		return BuildRCommand(bundleDir, port, bindHost)
	}
	expr := fmt.Sprintf(
		"options(shiny.autoreload=TRUE); shiny::runApp('.', host='%s', port=%d, launch.browser=FALSE)",
		bindHost, port)
	return []string{"Rscript", "--vanilla", "-e", expr}
}
