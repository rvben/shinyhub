# Deployment plan

`shinyhub plan` is the read-only answer to “what will this deploy do?” It builds
the exact archive `shinyhub deploy` would upload, validates the same launch
contract and `shinyhub.toml`, checks the selected remote app and your exact
permission, and compares the bundle with the newest successful deployment.

The recommended remote workflow is:

```bash
shinyhub doctor .
shinyhub plan .
shinyhub deploy . --open
```

Pass `.` explicitly. Like deploy, plan never assumes that the current directory
is safe to bundle. `--slug`, `--git`, `--branch`, `--subdir`, `--visibility`,
`--start`, `--host`, and `--wait-for-server` have the same meaning as they do
for deploy. The final line is a copy-pasteable deploy command containing the
source choices that produced the plan. It uses `--wait` because the plan is also
an automation primitive; for an interactive browser handoff, replace it with
`--open` (which already implies `--wait` and `--start`).

## What the plan shows

- whether the app will be created, updated, or redeployed with identical content;
- the target host, URL, create/manage permission, visibility, and start/stop effect;
- the canonical `sha256:` content digest used by the server;
- source and compressed sizes, included files (a concise list for people; the
  complete list in JSON), and paths excluded by an ignore file or ShinyHub's
  data/cache safety policy;
- the runtime, launch command, dependency preparation, readiness endpoint, and
  startup deadline;
- every declared manifest effect, including app settings, hooks, schedules,
  group access, and tracing;
- warnings for an existing app's ignored `--visibility`, fleet ownership, an
  in-flight replica redeploy, version skew, or a runtime the server does not report.

Plan makes only unauthenticated or authenticated `GET` requests. It never
creates an app, uploads a bundle, changes access, or starts a stopped app. A new
app is checked through your reported create permission without a probe write.

An unchanged digest means the files, executable bits, and manifest in the
bundle match the newest successful deployment. Running deploy is still an
explicit action: it records another deployment and follows the reported
lifecycle, which may restart replicas. On an older server that does not expose
a live content digest, plan reports the comparison as `unknown` instead of
claiming a match.

The command shown under Launch is the bundle-resolved base command. `{host}`
and `{port}` are assigned per replica; the selected server runtime and tracing
policy may wrap that base command. Readiness and manifest declarations are the
bundle's effective values.

## Automation

Use explicit JSON for a stable machine-readable document:

```bash
shinyhub plan . --output json
```

It contains `action`, `change_status`, nullable `changes`, remote state, bundle,
launch and manifest objects, warnings, `deploy_command`, and `exit_code`.

For a CI gate:

```bash
shinyhub plan . --detailed-exitcode --output json
# --fail-on-changes is an equivalent, CI-friendly spelling
```

| Code | Meaning |
|---:|---|
| `0` | Plan printed; with detailed exit codes, content is unchanged |
| `1` | Local validation or CLI/server protocol compatibility failed |
| `2` | Detailed mode only: content is new, changed, or cannot be compared |
| `3` | Network, authentication, or authorization failed |
| `6` | The host answered, but ShinyHub was not ready |

`unknown` returns `2` in detailed mode because CI cannot safely prove that a
deployment would leave content unchanged. Without a detailed-exit-code flag,
every successfully printed plan returns `0`.
