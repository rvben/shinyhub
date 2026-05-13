package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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

// Manifest is the decoded shinyhub.toml.
type Manifest struct {
	Hooks []Hook `toml:"hook"`
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
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ManifestFilename, err)
	}
	for i, h := range m.Hooks {
		if err := validateHook(h); err != nil {
			return nil, fmt.Errorf("%s [[hook]] #%d: %w", ManifestFilename, i+1, err)
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
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = logOut
	cmd.Stderr = logOut
	return cmd.Run()
}
