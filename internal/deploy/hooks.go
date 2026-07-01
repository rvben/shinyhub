package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mattn/go-shellwords"
	"github.com/rvben/shinyhub/internal/autoscalespec"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// ManifestFilename is the canonical bundle manifest name. It lives at the
// bundle root and is optional — bundles without one deploy exactly as before.
const ManifestFilename = "shinyhub.toml"

// HookTrigger identifies when a hook should fire in the deploy lifecycle.
// Only "post-deploy" is recognised today; unknown values are reported as an
// error at parse time so a typo doesn't silently no-op the hook.
type HookTrigger string

const (
	HookPostDeploy HookTrigger = "post-deploy"
)

// Hook is a single declarative command in shinyhub.toml.
type Hook struct {
	// On is the lifecycle trigger. Required; only "post-deploy" is accepted.
	On HookTrigger `toml:"on"`
	// Command is the argv to exec. Required; the first element is the
	// program path and is resolved against the bundle dir's PATH.
	Command []string `toml:"command"`
	// Timeout caps a single hook's wall-clock runtime. Defaults to
	// defaultHookTimeout when zero or unset.
	Timeout time.Duration `toml:"timeout"`
}

// AppSettings mirrors the [app] section. Pointer fields distinguish
// "absent" (nil) from "explicit value". HibernateResetToDefault is a
// parsed-out signal for `hibernate_timeout_minutes = -1` since TOML
// has no null literal: the convention mirrors the CLI's
// `--hibernate-timeout -1`.
type AppSettings struct {
	HibernateTimeoutMinutes *int `toml:"hibernate_timeout_minutes"`
	Replicas                *int `toml:"replicas"`
	MaxSessionsPerReplica   *int `toml:"max_sessions_per_replica"`

	// Autoscale declares the per-app session-saturation autoscale policy. A
	// non-nil pointer means the block is present and reconciles atomically into
	// the four autoscale_* columns on every deploy (like Replicas); nil means
	// "not declared" and leaves the stored policy - including anything set via
	// `apps set --autoscale` - untouched. Declaring it in the bundle lets the
	// policy travel with the app and survive rebuild-from-config hosts, instead
	// of having to be re-applied imperatively after each deploy.
	Autoscale *AutoscaleSettings `toml:"autoscale"`

	// IdentityHeaders opts this app out of (or explicitly into) identity
	// forwarding. nil = inherit the global auth.identity_headers flag.
	// Reconciled into apps.identity_headers on every deploy; removing the
	// key reverts to NULL (inherit). The global false kill switch always wins.
	IdentityHeaders *bool `toml:"identity_headers"`

	// MinWarmReplicas sets the pre-warming floor: replicas kept running
	// through idle hibernation. nil = leave the stored value unchanged.
	MinWarmReplicas *int `toml:"min_warm_replicas"`

	// Command overrides launch-command inference. Validated at parse time
	// (and again at boot, covering rollbacks); placeholders {port}, {host},
	// {data_dir} are substituted per replica at boot. With a command set,
	// type detection, uv-sync, and tracing auto-instrumentation are skipped.
	Command []string `toml:"command"`

	// StartupTimeoutSeconds lengthens (or shortens) the readiness deadline the
	// deploy health check allows before declaring the app crashed. nil =
	// inherit the platform default. Like Command and [tracing] auto it is read
	// from the bundle at every boot (deploy, redeploy, wake, scale, rollback),
	// so a slow-warming app's deadline travels with its bundle. It is never
	// reconciled into the DB.
	StartupTimeoutSeconds *int `toml:"startup_timeout_seconds"`

	// BuildTimeoutSeconds bounds the environment build (uv sync / renv::restore)
	// the host runs before the app process starts. nil = inherit the platform
	// default. Read from the bundle at every boot and never reconciled into the
	// DB (like StartupTimeoutSeconds). Distinct from StartupTimeoutSeconds, which
	// bounds only the post-build readiness window. Inert under the Docker runtime
	// (the build runs in-container).
	BuildTimeoutSeconds *int `toml:"build_timeout_seconds"`

	// MemoryLimitMB / CPUQuotaPercent cap each replica's resources. They
	// reconcile into apps.memory_limit_mb / apps.cpu_quota_percent on every
	// deploy (declared-only, like Replicas: nil leaves the stored value
	// unchanged). 0 = explicit unlimited; a positive value maps to cgroup v2
	// memory.max / cpu.max per replica (cpu_quota_percent 100 = 1 full core).
	// There is no manifest form for NULL (inherit-global); clear via
	// `apps set --memory-limit-mb -1` / `--cpu-quota-percent -1`.
	MemoryLimitMB   *int `toml:"memory_limit_mb"`
	CPUQuotaPercent *int `toml:"cpu_quota_percent"`

	HibernateResetToDefault bool `toml:"-"`
}

// AutoscaleSettings mirrors the [app] autoscale inline table. The block is an
// atomic unit: when present, all four columns are reconciled together (matching
// the PATCH /api/apps autoscale object and `SetAppAutoscale`). Enabled is a
// pointer so a declared block must state it explicitly - this rejects an
// incomplete block like `{ target = 0.9 }` that would otherwise persist an
// incoherent all-zero policy. Target is a fraction (0,1] of the per-replica
// session cap; 0 inherits the runtime default.
type AutoscaleSettings struct {
	Enabled     *bool   `toml:"enabled"`
	MinReplicas int     `toml:"min_replicas"`
	MaxReplicas int     `toml:"max_replicas"`
	Target      float64 `toml:"target"`
}

// Command and StartupTimeoutSeconds are not part of IsZero: they are read at
// boot, not reconciled into the DB. MemoryLimitMB / CPUQuotaPercent ARE
// reconciled into the DB (like Replicas), so they count.
func (a AppSettings) IsZero() bool {
	return a.HibernateTimeoutMinutes == nil &&
		a.Replicas == nil &&
		a.MaxSessionsPerReplica == nil &&
		a.IdentityHeaders == nil &&
		a.MinWarmReplicas == nil &&
		a.MemoryLimitMB == nil &&
		a.CPUQuotaPercent == nil &&
		a.Autoscale == nil &&
		!a.HibernateResetToDefault
}

// ScheduleSpec mirrors one [[schedule]] block, post-resolution: Command
// is set from either Cmd (shell-parsed) or CmdJSON (JSON-parsed) during
// LoadManifest so the application layer doesn't re-parse and never sees
// an unparseable manifest reach the DB.
type ScheduleSpec struct {
	Name           string `toml:"name"`
	Cron           string `toml:"cron"`
	Cmd            string `toml:"cmd"`
	CmdJSON        string `toml:"cmd_json"`
	TimeoutSeconds *int   `toml:"timeout_seconds"`
	Overlap        string `toml:"overlap"`
	Missed         string `toml:"missed"`
	Disabled       bool   `toml:"disabled"`
	// Timezone is an optional IANA timezone for the schedule. Empty means
	// "inherit the server default". Validated against time.LoadLocation at
	// manifest parse time.
	Timezone string `toml:"timezone"`

	// RunOnRegister, when true, fires this schedule once immediately the first
	// time it is registered on an app that has never had a successful run of it
	// - warming the app's cache on a fresh deploy. It is a deploy-time
	// instruction, never persisted; the gate lives in the server.
	RunOnRegister bool `toml:"run_on_register"`

	Command []string `toml:"-"`
}

// AppAccess declares per-app group access rules in the manifest. Groups in
// viewer_groups get the viewer role; groups in manager_groups get manager.
// These reconcile into app_group_access as source='manifest' on each deploy.
type AppAccess struct {
	ViewerGroups  []string `toml:"viewer_groups"`
	ManagerGroups []string `toml:"manager_groups"`
}

func (a AppAccess) IsZero() bool {
	return len(a.ViewerGroups) == 0 && len(a.ManagerGroups) == 0
}

// TracingSettings mirrors the [tracing] section. Auto overrides the fleet's
// tracing.auto_instrument_apps default in either direction; nil (section or
// key absent) means "inherit the fleet default". The override is re-read from
// the bundle at every boot, so it travels with each deployed version,
// including rollbacks.
type TracingSettings struct {
	Auto *bool `toml:"auto"`
}

// Manifest is the decoded shinyhub.toml.
type Manifest struct {
	App       AppSettings     `toml:"app"`
	Hooks     []Hook          `toml:"hook"`
	Schedules []ScheduleSpec  `toml:"schedule"`
	Access    AppAccess       `toml:"access"`
	Tracing   TracingSettings `toml:"tracing"`
}

// defaultHookTimeout caps an individual hook's wall-clock runtime when the
// manifest does not specify one. Long enough for a database migration on a
// real bundle, short enough that a runaway hook doesn't pin the deploy lock
// forever.
const defaultHookTimeout = 5 * time.Minute

// LoadManifest reads shinyhub.toml from bundleDir. Returns (nil, nil) when
// no manifest is present so callers can treat the file as optional. A
// malformed manifest is fatal: deploys must not silently skip declared
// hooks because the operator mistyped a field.
func LoadManifest(bundleDir string) (*Manifest, error) {
	path := filepath.Join(bundleDir, ManifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", ManifestFilename, err)
	}
	var m Manifest
	meta, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", ManifestFilename, err)
	}
	// Strict-mode: reject any key that did not map to a known struct field.
	// Catches typos and future-compatibility mismatches at deploy time rather
	// than silently ignoring them.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("parse %s: unknown field(s): %s", ManifestFilename, strings.Join(keys, ", "))
	}
	for i, h := range m.Hooks {
		if err := validateHook(h); err != nil {
			return nil, fmt.Errorf("%s [[hook]] #%d: %w", ManifestFilename, i+1, err)
		}
	}
	if err := normalizeAndValidateApp(&m.App); err != nil {
		return nil, fmt.Errorf("%s [app]: %w", ManifestFilename, err)
	}
	seen := make(map[string]bool, len(m.Schedules))
	for i := range m.Schedules {
		if err := resolveAndValidateSchedule(&m.Schedules[i]); err != nil {
			return nil, fmt.Errorf("%s [[schedule]] #%d: %w", ManifestFilename, i+1, err)
		}
		name := m.Schedules[i].Name
		if seen[name] {
			return nil, fmt.Errorf("%s [[schedule]] #%d: duplicate name %q", ManifestFilename, i+1, name)
		}
		seen[name] = true
	}
	for _, g := range append(append([]string{}, m.Access.ViewerGroups...), m.Access.ManagerGroups...) {
		if strings.TrimSpace(g) == "" {
			return nil, fmt.Errorf("manifest [access]: group names must be non-empty")
		}
	}
	return &m, nil
}

func validateHook(h Hook) error {
	switch h.On {
	case HookPostDeploy:
	case "":
		return errors.New("missing `on`")
	default:
		return fmt.Errorf("unknown trigger %q (supported: post-deploy)", h.On)
	}
	if len(h.Command) == 0 {
		return errors.New("missing `command`")
	}
	for _, arg := range h.Command {
		if arg == "" {
			return errors.New("`command` contains an empty arg")
		}
	}
	if h.Timeout < 0 {
		return fmt.Errorf("negative timeout %s", h.Timeout)
	}
	return nil
}

func normalizeAndValidateApp(a *AppSettings) error {
	if a.HibernateTimeoutMinutes != nil {
		switch v := *a.HibernateTimeoutMinutes; {
		case v == -1:
			a.HibernateResetToDefault = true
			a.HibernateTimeoutMinutes = nil
		case v < 0:
			return fmt.Errorf("hibernate_timeout_minutes must be -1 (reset to default), 0 (disable), or a positive number; got %d", v)
		}
	}
	if a.Replicas != nil && *a.Replicas < 1 {
		return fmt.Errorf("replicas must be >= 1, got %d", *a.Replicas)
	}
	if a.MaxSessionsPerReplica != nil && (*a.MaxSessionsPerReplica < 0 || *a.MaxSessionsPerReplica > 1000) {
		return fmt.Errorf("max_sessions_per_replica must be between 0 and 1000, got %d", *a.MaxSessionsPerReplica)
	}
	if a.MinWarmReplicas != nil && (*a.MinWarmReplicas < 0 || *a.MinWarmReplicas > 1000) {
		return fmt.Errorf("min_warm_replicas must be between 0 and 1000, got %d", *a.MinWarmReplicas)
	}
	if a.StartupTimeoutSeconds != nil && (*a.StartupTimeoutSeconds < 1 || *a.StartupTimeoutSeconds > 3600) {
		return fmt.Errorf("startup_timeout_seconds must be between 1 and 3600, got %d", *a.StartupTimeoutSeconds)
	}
	if a.BuildTimeoutSeconds != nil && (*a.BuildTimeoutSeconds < 30 || *a.BuildTimeoutSeconds > 7200) {
		return fmt.Errorf("build_timeout_seconds must be between 30 and 7200, got %d", *a.BuildTimeoutSeconds)
	}
	if a.MemoryLimitMB != nil {
		if err := ValidateMemoryLimitMB(*a.MemoryLimitMB); err != nil {
			return err
		}
	}
	if a.CPUQuotaPercent != nil {
		if err := ValidateCPUQuotaPercent(*a.CPUQuotaPercent); err != nil {
			return err
		}
	}
	if a.Command != nil {
		if err := validateCommandTemplate(a.Command); err != nil {
			return err
		}
	}
	if a.Autoscale != nil {
		if err := validateAutoscale(*a.Autoscale); err != nil {
			return err
		}
	}
	return nil
}

// validateAutoscale enforces the parse-time bounds of a declared [app]
// autoscale block via the shared autoscalespec validator (also used by the
// fleet manifest), minus the runtime MaxReplicas ceiling, which needs server
// config and is checked in validateManifestForServer.
func validateAutoscale(as AutoscaleSettings) error {
	return autoscalespec.Validate(autoscalespec.Params{
		Enabled:     as.Enabled,
		MinReplicas: as.MinReplicas,
		MaxReplicas: as.MaxReplicas,
		Target:      as.Target,
	})
}

func resolveAndValidateSchedule(s *ScheduleSpec) error {
	if s.Cmd != "" && s.CmdJSON != "" {
		return errors.New("specify exactly one of `cmd` or `cmd_json`")
	}
	switch {
	case s.CmdJSON != "":
		var argv []string
		if err := json.Unmarshal([]byte(s.CmdJSON), &argv); err != nil {
			return fmt.Errorf("parse cmd_json: %w", err)
		}
		s.Command = argv
	case s.Cmd != "":
		argv, err := shellwords.Parse(s.Cmd)
		if err != nil {
			return fmt.Errorf("parse cmd: %w", err)
		}
		s.Command = argv
	default:
		return errors.New("one of `cmd` or `cmd_json` is required")
	}

	timeout := 3600
	if s.TimeoutSeconds != nil {
		timeout = *s.TimeoutSeconds
	}
	overlap := s.Overlap
	if overlap == "" {
		overlap = "skip"
	}
	missed := s.Missed
	if missed == "" {
		missed = "skip"
	}

	if err := schedulespec.Validate(s.Name, s.Cron, s.Timezone, s.Command, timeout, overlap, missed); err != nil {
		return err
	}

	if s.TimeoutSeconds == nil {
		s.TimeoutSeconds = &timeout
	}
	if s.Overlap == "" {
		s.Overlap = overlap
	}
	if s.Missed == "" {
		s.Missed = missed
	}
	return nil
}

// PostDeploy returns the subset of hooks that should fire after dependency
// installation but before app processes start. Order is preserved.
func (m *Manifest) PostDeploy() []Hook {
	if m == nil {
		return nil
	}
	var out []Hook
	for _, h := range m.Hooks {
		if h.On == HookPostDeploy {
			out = append(out, h)
		}
	}
	return out
}

// hookRunner is the test seam: production runs hooks via exec.CommandContext,
// tests substitute a function that records invocations without spawning real
// processes. Kept as a package-level var so a test can swap it in t.Cleanup.
var hookRunner = runHookExec

// RunPostDeployHooks executes each hook sequentially in bundleDir, streaming
// stdout/stderr to logOut. It stops on the first failure so a deploy never
// proceeds past a broken setup step. Hooks inherit `extraEnv` on top of the
// parent process env so callers can inject the same variables the app will
// see at start (PORT excluded — that's per-replica).
func RunPostDeployHooks(ctx context.Context, bundleDir string, hooks []Hook, extraEnv []string, logOut io.Writer) error {
	for i, h := range hooks {
		timeout := h.Timeout
		if timeout == 0 {
			timeout = defaultHookTimeout
		}
		fmt.Fprintf(logOut, "▶ hook[%d]: %s (timeout %s)\n", i, strings.Join(h.Command, " "), timeout)
		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		err := hookRunner(hookCtx, bundleDir, h.Command, extraEnv, logOut)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("hook[%d] (%s) timed out after %s", i, strings.Join(h.Command, " "), timeout)
			}
			return fmt.Errorf("hook[%d] (%s): %w", i, strings.Join(h.Command, " "), err)
		}
	}
	return nil
}

// runHookExec is the production implementation of hookRunner. Stdout and
// stderr are merged into logOut so the deploy log keeps a single linear
// transcript matching what an operator would see if they ran the hook
// manually with `2>&1`.
func runHookExec(ctx context.Context, bundleDir string, argv []string, extraEnv []string, logOut io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = bundleDir
	// Hooks are deployer-controlled code. Base the env on the scrubbed server
	// env (no SHINYHUB_* secrets), then layer the app's own env vars on top
	// so the hook sees what the app will see at start, minus server secrets.
	cmd.Env = append(process.SanitizedEnv(), extraEnv...)
	cmd.Stdout = logOut
	cmd.Stderr = logOut
	return cmd.Run()
}
