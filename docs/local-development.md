---
description: "Use one safe development command for a standalone app, every local app in a fleet, or an explicit remote ShinyHub host."
---

# Develop applications

`shinyhub dev` is the front door for app development:

```bash
shinyhub dev .
```

Your directory chooses the scope. Local execution is always the default; adding
`--remote <host>` is the only way to move the loop to a ShinyHub server.

<div class="shiny-dev-model" markdown>

<div markdown>

**One app · Local**

```bash
shinyhub dev .
```

Run the current app through a production-shaped local route.

</div>

<div markdown>

**Fleet · Local**

```bash
shinyhub dev .
```

At a fleet root, run every app with a watchable local source.

</div>

<div markdown>

**One app · Remote**

```bash
shinyhub dev . --remote dev
```

Attach the current app to an existing target on the named host.

</div>

<div markdown>

**Fleet · Remote**

```bash
shinyhub dev . --remote dev
```

Preflight every selected target before deploying the first change.

</div>

</div>

Use `--app <slug>` to narrow a fleet. Use `--create` or `--ephemeral --ttl 8h`
only when you deliberately want remote mode to create an app.

## The safe local loop

From a bundle containing `app.py`, `app.R`, or a `shinyhub.toml` command:

```bash
shinyhub doctor . --local
shinyhub dev . --open
```

[`doctor`](doctor.md) reports bundle, manifest, entrypoint, and runtime blockers
together without starting a process or contacting a server. `dev` then:

1. mirrors the source into an isolated generated workspace;
2. installs Python or R dependencies when needed;
3. starts the app behind ShinyHub's real local proxy;
4. waits for the declared readiness contract; and
5. watches for the next edit.

The source directory is treated as read-only. Generated `pyproject.toml`,
`uv.lock`, `.venv`, renv files, bytecode, and the `data` link stay outside the
checkout and can never leak into a deployment bundle. App data persists across
restarts in a separate directory.

Each edit starts as a candidate. The proxy switches only after that candidate
becomes healthy, so a syntax error, missing dependency, crash, or failed
readiness check leaves the last healthy version serving. Fix the file and the
next save retries automatically.

Requests use the production-shaped `/app/<slug>/` route with prefix stripping,
forwarding headers, WebSocket support, and cookie handling. The root URL
redirects to the app route.

## Common options

```bash
shinyhub dev . --open                 # open after the app is healthy
shinyhub dev . --fresh                # rebuild generated state; keep app data
shinyhub dev . --no-sync              # skip explicit uv/renv preparation
shinyhub dev . --port 8000            # choose the public proxy port
shinyhub dev . --slug sales           # serve at /app/sales/
shinyhub dev . --data-dir ../dev-data # choose durable app-data storage
shinyhub dev . --state-dir /tmp/sales # choose generated workspace state
```

Environment values come from `.env` by default and may be overridden with
repeatable `--env KEY=VALUE` flags. Values are never printed; diagnostics list
keys only. `PORT`, `SHINYHUB_APP_DATA`, and `SHINYHUB_APP_SLUG` are managed by
ShinyHub and cannot be overridden.

Use `[app] readiness_path` when `/` is not a meaningful health endpoint. By
default, any 2xx or 3xx response is healthy; add `readiness_status` to require
one exact status:

```toml
[app]
readiness_path = "/health/ready"
readiness_status = 204
startup_timeout_seconds = 180
```

Only one runner uses a cached workspace at a time. Give concurrent copies of
the same source distinct `--state-dir` values.

## Choose the scope

- [Fleet development](development/fleet.md) explains automatic manifest
  discovery, shared inputs, multi-app output, and selection.
- [Remote development](development/remote.md) explains connection, creation,
  safety, deployment history, logs, and traces.

`shinyhub run` remains the lower-level standalone command for one-shot checks
and specialist controls:

```bash
shinyhub run . --check       # boot smoke test, then exit
shinyhub run . --no-reload   # run once without watching files
```

New interactive workflows should use `shinyhub dev` so every app shares one
mental model. To work on the ShinyHub platform rather than an application, use
the separate [contributor development setup](contributing/development.md).
