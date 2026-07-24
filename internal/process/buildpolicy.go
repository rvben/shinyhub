package process

// buildInterpreterPolicy holds the server `build:` interpreter policy as
// UV_PYTHON_* KEY=value strings (config.BuildConfig.UVBuildEnv). It is applied
// as the outermost env layer of every NATIVE uv invocation - the host build
// (uv sync), the uv init/add project-synthesis step, serve-time `uv run`, and
// host-side post-deploy hooks - so the host policy wins over any per-app or
// service-env value (os/exec resolves duplicate keys by last occurrence).
//
// Two properties are deliberate:
//
//   - It lives only in process memory and is never written to the OS
//     environment. A zero-downtime re-exec hands the successor os.Environ(), so
//     an os.Setenv'd value would be inherited and an emptied config field could
//     never unset it; instead the successor recomputes the policy from the
//     freshly loaded config, and removing a `build:` key takes effect on the
//     next handoff.
//   - It is NOT folded into SanitizedEnv. The Docker/Fargate runtimes bake
//     their interpreter into the image and share SanitizedEnv; a host UV_PYTHON
//     pin (e.g. an absolute interpreter path) must not leak into a container
//     that cannot honor it.
//
// Empty means no host policy is configured; every helper is then a no-op.
var buildInterpreterPolicy []string

// SetBuildInterpreterPolicy records the server `build:` interpreter policy.
// Call once at serve startup (and again after a config reload). Passing nil or
// an empty slice clears it. It copies the slice and does not touch os.Environ.
func SetBuildInterpreterPolicy(env []string) {
	buildInterpreterPolicy = append([]string(nil), env...)
}

// WithBuildInterpreterPolicy appends the host build interpreter policy to env so
// its keys are authoritative over anything already present (a per-app or
// service-env value earlier in the slice). Callers pass the fully layered
// native uv env (scrubbed base + per-app vars + sandbox redirects); the policy
// keys are disjoint from the sandbox redirects, so appending last only
// overrides an app-set UV_PYTHON_*. Returns env unchanged when no policy is
// configured.
func WithBuildInterpreterPolicy(env []string) []string {
	if len(buildInterpreterPolicy) == 0 {
		return env
	}
	return append(env, buildInterpreterPolicy...)
}
