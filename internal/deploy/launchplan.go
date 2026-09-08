package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rvben/shinyhub/internal/process"
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
	// ReadyStatus 0 accepts 2xx/3xx; otherwise the exact status is required.
	ReadyStatus int
	DepPrep     []DepPrepStep
	Timeout     time.Duration
}

// LaunchOptions are the Manager-free inputs both consumers supply. See the
// design spec section 4.2 for the PrepHostDeps vs CommandHostDeps distinction.
type LaunchOptions struct {
	AppPath               string   // canonical external route prefix, e.g. /app/sales
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
		ReadyPath: defaultReadinessPath(m),
		Timeout:   defaultHealthTimeout,
	}
	// The renv policy is decided by the bundle, before the command is, because
	// renv activates at R startup no matter which command started R - including
	// a bundle's own [app] command, which returns below without ever reaching
	// type detection. renv reads its configuration while the project profile is
	// sourced, so it has to be environment: an options() call in the launch
	// expression runs too late to affect activation.
	plan.Env = append(plan.Env, process.RenvPolicyEnvFor(bundleDir)...)
	if m != nil && m.App.StartupTimeoutSeconds != nil {
		plan.Timeout = time.Duration(*m.App.StartupTimeoutSeconds) * time.Second
	}
	if m != nil {
		if m.App.ReadinessPath != "" {
			plan.ReadyPath = m.App.ReadinessPath
		}
		if m.App.ReadinessStatus != nil {
			plan.ReadyStatus = *m.App.ReadinessStatus
		}
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
	if m != nil && m.App.Framework != "" {
		entry := "app.py"
		if m.App.Framework == "plumber" {
			appType = "r"
			entry = "plumber.R"
		} else {
			appType = "python"
		}
		if info, err := os.Stat(filepath.Join(bundleDir, entry)); err != nil || info.IsDir() {
			return nil, fmt.Errorf("framework %s requires %s at the bundle root", m.App.Framework, entry)
		}
		if m.App.Framework == "fastapi" {
			_, projectErr := os.Stat(filepath.Join(bundleDir, "pyproject.toml"))
			_, requirementsErr := os.Stat(filepath.Join(bundleDir, "requirements.txt"))
			if projectErr != nil && requirementsErr != nil {
				return nil, fmt.Errorf("FastAPI requires requirements.txt or pyproject.toml declaring fastapi and uvicorn")
			}
		}
	}
	plan.AppType = appType
	switch appType {
	case "python":
		if opts.PrepHostDeps {
			plan.DepPrep = []DepPrepStep{
				{
					Label: "ensure project",

					Run: func(ctx context.Context, bundleDir string) error {
						if err := ensureProjectFn(ctx, bundleDir); err != nil {
							return fmt.Errorf("uv sync: prepare Python project: %w", err)
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
		if m != nil && m.App.Framework == "fastapi" {
			plan.Command = buildFastAPICommand(bundleDir, opts.Port, bindHost, opts.AppPath, auto, opts.CommandHostDeps, opts.Reload)
		} else {
			plan.Command = withPythonReload(buildCommandFn(bundleDir, opts.Port, opts.Workers, bindHost, auto, opts.CommandHostDeps), opts.Reload)
		}
	case "r":
		if opts.PrepHostDeps {
			plan.DepPrep = []DepPrepStep{{Label: "renv restore", Run: func(ctx context.Context, dir string) error {
				return rSyncFn(ctx, dir, opts.AppEnv)
			}}}
		}
		if m != nil && m.App.Framework == "plumber" {
			plan.Command = buildPlumberCommand(bundleDir, opts.Port, bindHost, opts.AppPath)
		} else {
			plan.Command = buildRCommandReload(bundleDir, opts.Port, bindHost, opts.Reload)
		}
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
	return rscriptCommand(bundleDir, expr)
}

// Both managed API launchers expose OpenAPI without requiring a root handler.
func defaultReadinessPath(m *Manifest) string {
	if m != nil && m.App.Framework != "" {
		return "/openapi.json"
	}
	return "/"
}

func buildFastAPICommand(bundleDir string, port int, bindHost, appPath string, autoInstrument, hostDeps, reload bool) []string {
	base := pythonCommandPrefix(bundleDir, autoInstrument, hostDeps)
	cmd := append(base, "uvicorn", "app:app", "--host", bindHost, "--port", fmt.Sprint(port))
	if appPath != "" {
		cmd = append(cmd, "--root-path", appPath)
	}
	if reload {
		cmd = append(cmd, "--reload")
	}
	return cmd
}

func buildPlumberCommand(bundleDir string, port int, bindHost, appPath string) []string {
	router := "plumber::pr('plumber.R')"
	prefix := ""
	if appPath != "" {
		// Plumber's documentation endpoint derives its server URL at request
		// time. Set the public URL without changing the internal route paths.
		prefix = fmt.Sprintf("options(plumber.apiURL=%q); ", appPath)
	}
	return rscriptCommand(bundleDir, prefix+fmt.Sprintf("plumber::pr_run(%s, host='%s', port=%d, docs=TRUE, swaggerCallback=function(...) NULL)", router, bindHost, port))
}
