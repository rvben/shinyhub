package deploy

import (
	"fmt"
	"time"
)

// DepPrepStep is one host-side preparation action (EnsureProject, uv sync,
// renv restore). The runner executes each in order before launch.
type DepPrepStep struct {
	Label string
	Run   func(bundleDir string) error
}

// LaunchPlan is the canonical description of how a single app replica launches.
// It is the one source of truth shared by the server boot path and `shinyhub run`.
type LaunchPlan struct {
	AppType         string
	Manifest        *Manifest
	Command         []string
	FallbackCommand []string // inferred Python only: command WITHOUT OTEL wrapping, when auto-instrument applied
	Env             []string // launch-coupled only ("PORT"); platform/per-app env layered by the consumer
	BindHost        string
	ReadyPath       string
	DepPrep         []DepPrepStep
	Timeout         time.Duration
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
	DataDir               string
	Reload                bool
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

// resolveInferred is completed in Task 3.
func resolveInferred(bundleDir, bindHost string, m *Manifest, opts LaunchOptions, plan *LaunchPlan) (*LaunchPlan, error) {
	return nil, fmt.Errorf("inferred launch not yet implemented")
}
