# Native process isolation

The native runtime launches each app as a plain OS process. By default that
process runs with the full filesystem reach of the ShinyHub service user. The
**isolation dial** narrows that reach without requiring the Docker runtime, using
[Landlock](https://docs.kernel.org/userspace-api/landlock.html) - the Linux
kernel's unprivileged, self-imposed access-control mechanism.

It is a **blast-radius boundary**, not a defense against determined hostile code.
If your threat model is genuinely untrusted app code in a multi-tenant setting,
run the [Docker runtime](../README.md#docker) instead, which adds process, user,
and network isolation.

## The dial

```yaml
runtime:
  native:
    isolation: standard   # off (default) | standard
```

Or via environment: `SHINYHUB_RUNTIME_NATIVE_ISOLATION=standard`.

| Level | Effect |
|-------|--------|
| `off` (default) | No isolation. The historical native behavior. |
| `standard` | Filesystem confinement (below) plus `NO_NEW_PRIVS` (blocks setuid privilege escalation). |

`strict` (tighter reads and network restriction) is reserved for a later release
and is rejected at load until then, rather than silently treated as `standard`.

## What `standard` does

The app process is left free to **read** the whole filesystem (so interpreters
and shared libraries load normally) but may **write** only to:

- its own deployment directory,
- its persistent per-app data directory (`storage.app_data_dir`), if configured,
- `/tmp` and `/dev` (scratch and device nodes such as `/dev/null`, `/dev/urandom`
  - dangerous device nodes stay protected by ordinary file permissions).

Everything else - other apps' bundles, the control-plane database, system
directories - is read-only to the app. `TMPDIR` is pointed at a private
directory inside the app's own tree so a well-behaved app gets an isolated
scratch area while `/tmp` remains available as a fallback.

To keep cache-writing launchers working under the read-only root, `TMPDIR`,
`UV_CACHE_DIR`, and `XDG_CACHE_HOME` are pointed at writable subdirectories of
the app's own tree. (`uv run` initializes a cache even with `--frozen
--no-sync`; without this redirect it would be denied and the app would fail to
start.)

Both the long-running app process and one-shot runs (scheduled jobs) are
confined the same way.

Enforcement is applied by a small re-exec step: ShinyHub launches the app through
a hidden `__sandbox` subcommand of its own binary, which imposes the Landlock
rules on itself and then executes the real app command. The app never sees the
sandbox policy in its environment.

## Requirements and graceful degradation

Isolation is **Linux-only** and **best-effort**:

- On a kernel with Landlock (roughly 5.13+, with the feature compiled in and
  active - e.g. Ubuntu's stock kernels), the confinement above is enforced.
- On an older kernel, or one without Landlock (some minimal/microVM kernels), the
  dial degrades to a no-op: the app still starts, just without confinement.
- On non-Linux builds there is no enforcement backend; configuring isolation logs
  a startup warning and runs without it.

Because Landlock only downgrades and never blocks startup, turning the dial on is
safe to roll out; where the kernel supports it, it takes effect, and where it does
not, apps keep running.

## Choosing native+isolation vs Docker

| | Native + `standard` | Docker runtime |
|---|---|---|
| Filesystem write confinement | yes (Landlock) | yes |
| Privilege-escalation block (`NO_NEW_PRIVS`) | yes | yes |
| Process / PID isolation | no | yes |
| Network isolation | no | yes (namespace) |
| Separate user boundary | no (same UID) | yes |
| Needs a container runtime | no | yes |

Reach for native isolation when you want meaningful hardening of the lightweight
native runtime; reach for Docker when you need full multi-tenant isolation.
